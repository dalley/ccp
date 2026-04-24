//go:build !windows

package allowlist

// Public invariants (load-bearing, do not weaken without updating Unit 10):
//
//   1. Hash is content-only: SHA-256 of the marker's bytes. The marker's
//      filesystem path is NOT in the hash. This is a deliberate divergence
//      from direnv's path+content scheme. Rationale: ccp's headline feature
//      is sync-across-machines; including $HOME-dependent paths in the hash
//      would force re-approval on every workstation. Key Decision 12 in the
//      v2.0 plan has the full threat-model analysis.
//
//   2. Hash/ReadName open files with O_RDONLY|O_NOFOLLOW and never
//      re-resolve the path after fstat. This closes the TOCTOU window: a
//      symlink swap between "does this exist" and "hash it" cannot redirect
//      the read to a different file. Symlinks at the marker path itself
//      return an error (ELOOP on Linux/macOS) — fail-closed.
//
//   3. Callers of Check do not need to hold the ccp state lock; Save is
//      atomic via rename(2), so a concurrent reader sees either the full
//      old state or the full new state, never a torn intermediate. This is
//      load-bearing for the `ccp shell-resolve-dir` hot path.
//
//   4. Approve and Revoke do NOT take the lock themselves — the CLI
//      wrapper wraps them with withLock. The package is pure file I/O;
//      concurrency discipline lives at the edge.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

// CurrentSchemaVersion is the allowlist.toml format this ccp binary writes.
// Bump and add a migration when the on-disk shape changes.
const CurrentSchemaVersion = 1

// maxMarkerBytes is the size cap enforced by Hash and ReadName. A valid
// marker is <70 bytes of content; 64 KiB gives enormous headroom while
// still refusing a pathological multi-megabyte file.
const maxMarkerBytes = 64 * 1024

// maxAncestors caps FindMarker's walk depth for defense in depth — a
// pathological CWD deep under bind mounts can't push us into an unbounded
// loop. 64 is far more than any realistic directory depth.
const maxAncestors = 64

// Status classifies the relationship between a marker file and the
// allowlist. It is the core result of Check.
type Status int

const (
	// StatusUnallowed means no entry exists for this path — the user has
	// not approved this marker. Default / fail-closed state.
	StatusUnallowed Status = iota
	// StatusAllowed means an entry exists and the on-disk hash matches.
	StatusAllowed
	// StatusHashMismatch means an entry exists but the on-disk hash
	// differs — benign (user edited the file) or hostile (supply-chain
	// attack via commit). Either way, user action is required.
	StatusHashMismatch
)

// String makes Status play nicely in error messages and test diagnostics.
func (s Status) String() string {
	switch s {
	case StatusUnallowed:
		return "Unallowed"
	case StatusAllowed:
		return "Allowed"
	case StatusHashMismatch:
		return "HashMismatch"
	default:
		return fmt.Sprintf("Status(%d)", int(s))
	}
}

// File is the on-disk shape of allowlist.toml. Mirrors manifest.Manifest.
type File struct {
	SchemaVersion int               `toml:"schema_version"`
	Entries       map[string]string `toml:"entries,omitempty"`
}

// Default returns an empty File ready for first-time Save.
func Default() File {
	return File{SchemaVersion: CurrentSchemaVersion}
}

// markerNameRe is the strict marker-content grammar. Profile names are
// lowercase-ASCII identifiers up to 63 chars. No leading digit, no Unicode,
// no case variation — matches Unit 8 spec and the v2.0 plan's Key Decision 21.
var markerNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// Load reads the allowlist at path. Missing file returns (Default(), false, nil)
// — intentional: a fresh machine has nothing allowed, which is fail-closed.
// Loading a schema newer than this binary returns an error with an
// "upgrade ccp" hint so a mixed-version user sees a real explanation.
func Load(path string) (File, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), false, nil
		}
		return File{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	var f File
	if err := toml.Unmarshal(b, &f); err != nil {
		return File{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.SchemaVersion == 0 {
		f.SchemaVersion = CurrentSchemaVersion
	}
	if f.SchemaVersion > CurrentSchemaVersion {
		return f, true, fmt.Errorf("allowlist schema %d is newer than this ccp (%d); upgrade ccp",
			f.SchemaVersion, CurrentSchemaVersion)
	}
	if f.Entries == nil {
		f.Entries = map[string]string{}
	}
	return f, true, nil
}

// Save writes f to path atomically via a sibling temp file + rename(2).
// The parent directory must exist. Callers mutating shared state should
// hold the ccp state lock.
func Save(path string, f File) error {
	if f.SchemaVersion == 0 {
		f.SchemaVersion = CurrentSchemaVersion
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".allowlist-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Clean up the temp file if anything below fails; os.Remove on an
	// already-renamed path is a harmless no-op (the rename consumed it).
	defer func() { _ = os.Remove(tmpName) }()

	// 0600: allow-list is per-machine trust state, no reason for group/other
	// to read it. Matches the secrets-dir permission discipline.
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	enc := toml.NewEncoder(tmp)
	if err := enc.Encode(f); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode toml: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}

// Hash opens path with O_RDONLY|O_NOFOLLOW (rejecting symlinks), fstats it
// for size sanity (<= maxMarkerBytes), and returns the SHA-256 of its
// contents prefixed with "sha256:" to allow future algorithm upgrades
// without a schema-version bump.
//
// The hash is content-only — identical bytes at any path produce identical
// output. See invariant 1 at the top of this file.
func Hash(path string) (string, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s: not a regular file", path)
	}
	if info.Size() > maxMarkerBytes {
		return "", fmt.Errorf("%s: marker is %d bytes, exceeds %d-byte cap",
			path, info.Size(), maxMarkerBytes)
	}

	h := sha256.New()
	// Cap the reader defensively — even if Size() lied (racy fs), we will
	// not hash more than maxMarkerBytes+1 bytes and will error on the tail.
	n, err := io.Copy(h, io.LimitReader(f, maxMarkerBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if n > maxMarkerBytes {
		return "", fmt.Errorf("%s: marker exceeds %d-byte cap during read", path, maxMarkerBytes)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// ReadName reads the marker at path, validates the strict single-line
// format, and returns the profile name.
//
// Accepts exactly: one line matching ^[a-z][a-z0-9_-]{0,62}$, optionally
// followed by a single trailing \n. Rejects UTF-8 BOM, CRLF, any
// whitespace that is not a final \n, multi-line content, and zero-width
// characters. Error wraps ErrInvalidMarker and names the byte offset of
// the first invalid character so the user can pinpoint the problem.
func ReadName(path string) (string, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s: not a regular file", path)
	}
	if info.Size() > maxMarkerBytes {
		return "", fmt.Errorf("%s: marker is %d bytes, exceeds %d-byte cap",
			path, info.Size(), maxMarkerBytes)
	}

	b, err := io.ReadAll(io.LimitReader(f, maxMarkerBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(b)) > maxMarkerBytes {
		return "", fmt.Errorf("%s: marker exceeds %d-byte cap during read", path, maxMarkerBytes)
	}

	name, verr := validateMarkerBytes(b)
	if verr != nil {
		return "", fmt.Errorf("%s: %w", path, verr)
	}
	return name, nil
}

// validateMarkerBytes enforces the strict single-line grammar. Returns the
// profile name on success, or an ErrInvalidMarker-wrapped error naming the
// byte offset of the first invalid character on failure.
//
// Isolated as a pure function so tests can pin down offset accounting
// without touching the filesystem.
func validateMarkerBytes(b []byte) (string, error) {
	// BOM check: explicitly called out so the error message is specific
	// (rather than a generic "invalid first character").
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return "", fmt.Errorf("%w: UTF-8 BOM at byte offset 0 (strip it and retry)",
			ErrInvalidMarker)
	}
	if len(b) == 0 {
		return "", fmt.Errorf("%w: file is empty", ErrInvalidMarker)
	}

	// Strip at most one trailing \n. Nothing else — a trailing space,
	// trailing \r, or bare CR are all rejected.
	body := b
	if body[len(body)-1] == '\n' {
		body = body[:len(body)-1]
	}

	// No second newline allowed anywhere in body (strict single line).
	if i := bytes.IndexByte(body, '\n'); i >= 0 {
		return "", fmt.Errorf("%w: unexpected newline at byte offset %d (marker must be a single line)",
			ErrInvalidMarker, i)
	}
	// No carriage return anywhere (catches CRLF line endings specifically).
	if i := bytes.IndexByte(body, '\r'); i >= 0 {
		return "", fmt.Errorf("%w: carriage return at byte offset %d (use LF line endings, not CRLF)",
			ErrInvalidMarker, i)
	}

	// Walk byte-by-byte and point at the first invalid rune. This catches
	// zero-width spaces, leading tabs, trailing spaces, and anything else
	// that isn't in the restricted ASCII set the regex accepts.
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c < 0x20 || c >= 0x80 {
			// Non-ASCII / control byte. Decode as UTF-8 for a friendlier
			// message when possible, but still surface the byte offset.
			r, _ := utf8.DecodeRune(body[i:])
			return "", fmt.Errorf("%w: non-ASCII or control character %q at byte offset %d",
				ErrInvalidMarker, r, i)
		}
	}

	name := string(body)
	if !markerNameRe.MatchString(name) {
		// Regex rejection — pinpoint the first byte that breaks the grammar.
		off := firstInvalidByte(body)
		return "", fmt.Errorf("%w: does not match ^[a-z][a-z0-9_-]{0,62}$ (first bad byte at offset %d: %q)",
			ErrInvalidMarker, off, byteAt(body, off))
	}
	return name, nil
}

// firstInvalidByte returns the byte offset of the first character in body
// that violates ^[a-z][a-z0-9_-]{0,62}$. Used only after we know the regex
// rejected the whole string, so we know *some* byte is invalid.
func firstInvalidByte(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	// First byte must be [a-z].
	c0 := body[0]
	if c0 < 'a' || c0 > 'z' {
		return 0
	}
	// Remaining bytes: [a-z0-9_-].
	for i := 1; i < len(body); i++ {
		c := body[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return i
	}
	// Length overflow (>63 chars) — the 64th byte is the violation.
	if len(body) > 63 {
		return 63
	}
	return len(body)
}

// byteAt returns the byte at off, or 0 for out-of-range. Small helper so
// error formatting can use %q without panicking on empty input.
func byteAt(b []byte, off int) byte {
	if off < 0 || off >= len(b) {
		return 0
	}
	return b[off]
}

// openNoFollow opens path with O_RDONLY|O_NOFOLLOW. On Linux and macOS,
// the syscall returns ELOOP when path itself is a symlink; we translate
// that into a user-friendly "refused to follow symlink" error.
func openNoFollow(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		// Surface the symlink refusal specifically; everything else gets
		// the raw error so "file not found" stays legible.
		if isSymlinkError(err) {
			return nil, fmt.Errorf("refused to follow symlink at %s", path)
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return f, nil
}

// isSymlinkError reports whether err from OpenFile with O_NOFOLLOW
// indicates the target was itself a symlink. Linux and macOS both return
// ELOOP in this case.
func isSymlinkError(err error) bool {
	var pe *os.PathError
	if errors.As(err, &pe) {
		if errno, ok := pe.Err.(syscall.Errno); ok {
			return errno == syscall.ELOOP
		}
	}
	return false
}

// FindMarker walks up from startDir looking for a `.claude-profile` file.
// Stops at the first marker found, or at homeDir, or at the nearest `.git`
// directory — whichever is nearest. Returns ("", nil) on not-found.
//
// Stops are defensive:
//   - homeDir prevents a runaway walk past $HOME into / (and crossing user
//     boundaries on shared systems).
//   - .git is a natural repo boundary — a marker outside your repo but
//     inside $HOME is almost always a mistake, and an attacker-placed
//     marker in $HOME itself would be extremely suspicious.
//   - maxAncestors caps walk depth at 64 levels for bind-mount weirdness.
func FindMarker(startDir, homeDir string) (string, error) {
	if startDir == "" {
		return "", fmt.Errorf("FindMarker: startDir is empty")
	}
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", startDir, err)
	}
	var absHome string
	if homeDir != "" {
		absHome, err = filepath.Abs(homeDir)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", homeDir, err)
		}
	}

	for depth := 0; depth < maxAncestors; depth++ {
		// Check for a marker in this directory.
		candidate := filepath.Join(dir, ".claude-profile")
		if info, err := os.Lstat(candidate); err == nil {
			// We don't follow symlinks here — that's Hash's job, and it
			// will reject the marker if it turns out to be a symlink. We
			// just report "here is something named .claude-profile."
			if info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return candidate, nil
			}
		} else if !os.IsNotExist(err) {
			// A permission error or similar: surface it rather than
			// silently walking past.
			return "", fmt.Errorf("stat %s: %w", candidate, err)
		}

		// Boundary: .git dir (repo root). Don't cross it.
		gitPath := filepath.Join(dir, ".git")
		if _, err := os.Lstat(gitPath); err == nil {
			return "", nil
		}

		// Boundary: $HOME. Don't cross it.
		if absHome != "" && dir == absHome {
			return "", nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Hit filesystem root.
			return "", nil
		}
		dir = parent
	}
	return "", nil
}

// Approve computes the hash of markerPath and upserts it into the
// allowlist at allowlistPath. The caller must hold the ccp state lock.
//
// Wrapping discipline: the CLI layer does
//
//	withLock(p, func() error { return allowlist.Approve(p.AllowlistPath, m) })
//
// The package itself is pure file I/O; it doesn't know how to find the
// lock file, so it can't take the lock.
func Approve(allowlistPath, markerPath string) error {
	hash, err := Hash(markerPath)
	if err != nil {
		return fmt.Errorf("hash marker: %w", err)
	}
	abs, err := filepath.Abs(markerPath)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", markerPath, err)
	}
	f, _, err := Load(allowlistPath)
	if err != nil {
		return err
	}
	if f.Entries == nil {
		f.Entries = map[string]string{}
	}
	f.Entries[abs] = hash
	return Save(allowlistPath, f)
}

// Revoke removes the entry for markerPath from the allowlist. Idempotent:
// removing a non-existent entry is a silent no-op (the end state matches
// the user's intent). The caller must hold the ccp state lock.
func Revoke(allowlistPath, markerPath string) error {
	abs, err := filepath.Abs(markerPath)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", markerPath, err)
	}
	f, existed, err := Load(allowlistPath)
	if err != nil {
		return err
	}
	if !existed {
		return nil
	}
	if _, ok := f.Entries[abs]; !ok {
		return nil
	}
	delete(f.Entries, abs)
	return Save(allowlistPath, f)
}

// Check returns the relationship between the stored entry (if any) and
// the current on-disk hash of markerPath. Lock-free by design: the hot
// path (`ccp shell-resolve-dir`) calls this on every cd, and taking the
// global flock there would serialize every prompt in every shell.
//
// The atomic-rename discipline in Save makes this safe: a reader sees
// either the complete pre-save file or the complete post-save file.
//
// Returns the marker's current hash alongside Status for informational
// display — callers can show the "approved X, current Y" diff on drift.
func Check(allowlistPath, markerPath string) (Status, string, error) {
	current, err := Hash(markerPath)
	if err != nil {
		return StatusUnallowed, "", fmt.Errorf("hash marker: %w", err)
	}
	abs, err := filepath.Abs(markerPath)
	if err != nil {
		return StatusUnallowed, current, fmt.Errorf("resolve %s: %w", markerPath, err)
	}
	f, _, err := Load(allowlistPath)
	if err != nil {
		return StatusUnallowed, current, err
	}
	stored, ok := f.Entries[abs]
	if !ok {
		return StatusUnallowed, current, ErrMarkerNotAllowed
	}
	if stored != current {
		return StatusHashMismatch, current, ErrMarkerHashMismatch
	}
	return StatusAllowed, current, nil
}
