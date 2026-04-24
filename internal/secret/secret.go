//go:build !windows

package secret

// Package secret implementation: keychain-first, file-fallback per-profile
// secret storage. See errors.go for the package doc comment and sentinel
// error contract.
//
// Concurrency and test invariants (load-bearing, do not weaken):
//
//  1. go-keyring's MockInit()/MockInitWithError() mutate package-level
//     state. Tests in this package MUST NOT call t.Parallel() anywhere —
//     mock state would race between parallel subtests. A top-of-file
//     comment in secret_test.go restates this for future maintainers.
//
//  2. This package is pure file I/O + keyring calls; it does NOT take the
//     ccp state lock. Callers (the `ccp secret` CLI) are expected to
//     wrap Set/Delete in withLock when concurrent mutation is possible.
//     Concurrent Gets are safe because our Save is atomic via rename(2)
//     and the keychain backends serialize internally.
//
//  3. Keychain enumeration: go-keyring exposes no List primitive, and the
//     POSIX backends it wraps don't have a portable one either. We keep a
//     sibling index file <SecretsDir>/<profile>.keychain-index.json that
//     records which keys were written to the keychain, so List can merge
//     without a backend enumeration API. The index is best-effort — a
//     user who `security delete-generic-password`s directly may see a
//     stale key in the index; Get will return ErrSecretNotFound for it
//     and the CLI's `rm` scrubs the index on Delete.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/dalley/ccp/internal/paths"
	"github.com/dalley/ccp/internal/profile"
	"github.com/zalando/go-keyring"
)

// service is the keychain service name used for every ccp entry. The
// keychain account field is the profile name; the key is encoded into the
// account as "<profile>/<key>" so a single service namespace holds all
// per-profile entries. Keeps keychain list views (Keychain.app, seahorse)
// legible and scoped.
const service = "ccp"

// maxKeyLen caps the key length at 255 bytes. Picked to stay comfortably
// under every keychain backend's account-field limit while matching the
// conventional env-var length sanity ceiling.
const maxKeyLen = 255

// keyRe matches the legal key grammar: env-var-like identifier, no colon
// (reserved by the {{ keychain:KEY }} ref parser), no control chars.
var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// warnWriter is where the one-time keychain-unavailable fallback warning
// goes. Defaults to os.Stderr so humans see it; tests swap it via
// SetFallbackWarnWriter. Mirrors internal/sync/auth.go:SetAuthWarnWriter.
var (
	warnWriter    io.Writer = os.Stderr
	warnWriterMu  sync.Mutex
	fallbackWarn  sync.Once
)

// SetFallbackWarnWriter redirects the "keychain unavailable" warning for
// tests. Default is os.Stderr. Pass io.Discard to silence in tests that
// don't care about the warning itself. Safe to call from any goroutine.
func SetFallbackWarnWriter(w io.Writer) {
	if w == nil {
		w = io.Discard
	}
	warnWriterMu.Lock()
	warnWriter = w
	warnWriterMu.Unlock()
}

// resetFallbackWarnOnce rearms the sync.Once so a second test can observe
// a fresh "first fall-through" warning. Package-internal — tests in this
// package use it directly; external callers have no reason to reset.
func resetFallbackWarnOnce() {
	fallbackWarn = sync.Once{}
}

// keychainState classifies the result of a keyring operation so callers
// can pick between "fall back to file store" and "surface a typed error".
type keychainState int

const (
	stateOK          keychainState = iota // operation succeeded
	stateNotFound                         // keyring.ErrNotFound
	stateLocked                           // locked / authentication refused
	stateUnavailable                      // no keyring backend reachable
	stateOther                            // anything else (wrap + fall through as unavailable)
)

// classifyKeyringErr maps a keyring error to a keychainState. nil → stateOK.
//
// Matching rules (documented in the Unit 3 spec):
//   - errors.Is(err, keyring.ErrNotFound) → stateNotFound.
//   - String match "locked" / "authentication" → stateLocked (macOS locked
//     keychain; "authentication" catches Secret Service's "org.freedesktop.
//     DBus.Error.AuthFailed").
//   - String match "no such interface" / "dbus" / "secret service" →
//     stateUnavailable (headless Linux without a running Secret Service).
//   - Anything else → stateOther. Callers treat this like stateUnavailable
//     for fallback purposes but surface the underlying error.
func classifyKeyringErr(err error) keychainState {
	if err == nil {
		return stateOK
	}
	if errors.Is(err, keyring.ErrNotFound) {
		return stateNotFound
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "locked") || strings.Contains(s, "authentication"):
		return stateLocked
	case strings.Contains(s, "no such interface"),
		strings.Contains(s, "dbus"),
		strings.Contains(s, "secret service not available"),
		strings.Contains(s, "cannot autolaunch"):
		return stateUnavailable
	}
	return stateOther
}

// account encodes (profile, key) into a keychain account-field string.
// Format: "<profile>/<key>". Profile names are validated upstream to
// ^[a-z][a-z0-9_-]{0,62}$ (no slashes) and keys to ^[A-Za-z_][A-Za-z0-9_]*$
// (no slashes), so the separator is unambiguous.
func account(profile, key string) string {
	return profile + "/" + key
}

// emitFallbackWarn prints the "keychain unavailable, writing to file"
// warning exactly once per process. The message names the underlying error
// and the on-disk path so the user understands why and where.
func emitFallbackWarn(filePath string, reason error) {
	fallbackWarn.Do(func() {
		warnWriterMu.Lock()
		w := warnWriter
		warnWriterMu.Unlock()
		fmt.Fprintf(w,
			"ccp: keychain unavailable (%s); storing in %s — install libsecret/gnome-keyring for keychain-backed storage\n",
			reason, filePath)
	})
}

// ValidateKey checks that key conforms to the ccp key grammar. Exported so
// the CLI can surface the rule in its argument parser; internal callers
// use validateNames which combines key + profile validation.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("secret key is empty")
	}
	if len(key) > maxKeyLen {
		return fmt.Errorf("secret key is %d bytes, exceeds %d-byte cap", len(key), maxKeyLen)
	}
	if strings.ContainsRune(key, ':') {
		return fmt.Errorf("secret key %q contains ':' (reserved by ref grammar)", key)
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return fmt.Errorf("secret key %q contains a control character", key)
		}
	}
	if !keyRe.MatchString(key) {
		return fmt.Errorf("secret key %q must match ^[A-Za-z_][A-Za-z0-9_]*$", key)
	}
	return nil
}

// validateNames runs profile and key validation together so every public
// entry point enforces the same rules in one place.
func validateNames(prof, key string) error {
	if err := profile.ValidateName(prof); err != nil {
		return err
	}
	return ValidateKey(key)
}

// Set stores value for key in the keychain. On stateUnavailable it falls
// back to the file store with a one-time stderr warning. On stateLocked
// it returns ErrKeychainLocked WITHOUT falling back (the user has a
// keychain; it needs unlocking). stateNotFound is impossible for Set.
func Set(p paths.Paths, prof, key, value string) error {
	if err := validateNames(prof, key); err != nil {
		return err
	}
	if err := p.Ensure(); err != nil {
		return err
	}

	err := keyring.Set(service, account(prof, key), value)
	switch classifyKeyringErr(err) {
	case stateOK:
		// Record the key in the keychain index so List can enumerate.
		return indexAdd(p, prof, key)
	case stateNotFound:
		// Impossible for Set in the happy path; if a backend ever produces
		// it here (e.g., a mock in an unusual state), surface it unwrapped
		// so the test author sees the contract violation.
		return fmt.Errorf("keyring.Set returned unexpected ErrNotFound: %w", err)
	case stateLocked:
		return ErrKeychainLocked
	case stateUnavailable:
		emitFallbackWarn(p.SecretFilePath(prof), err)
		return fileSet(p, prof, key, value)
	case stateOther:
		// Treat as unavailable for fallback purposes but surface the
		// underlying error as a hint so the user can investigate.
		emitFallbackWarn(p.SecretFilePath(prof), err)
		return fileSet(p, prof, key, value)
	}
	return fmt.Errorf("unreachable: classifyKeyringErr returned unknown state")
}

// Get reads value for key from the keychain; on stateNotFound or
// stateUnavailable it falls through to the file store. stateLocked
// returns ErrKeychainLocked without falling through (we can't know if the
// key exists in the locked keychain). Missing in both backends →
// ErrSecretNotFound.
func Get(p paths.Paths, prof, key string) (string, error) {
	if err := validateNames(prof, key); err != nil {
		return "", err
	}

	val, err := keyring.Get(service, account(prof, key))
	switch classifyKeyringErr(err) {
	case stateOK:
		return val, nil
	case stateLocked:
		return "", ErrKeychainLocked
	case stateNotFound:
		// Fall through to file store.
		v, ferr := fileGet(p, prof, key)
		if ferr == nil {
			return v, nil
		}
		if errors.Is(ferr, ErrSecretNotFound) {
			return "", ErrSecretNotFound
		}
		return "", ferr
	case stateUnavailable:
		v, ferr := fileGet(p, prof, key)
		if ferr == nil {
			return v, nil
		}
		if errors.Is(ferr, ErrSecretNotFound) {
			return "", ErrSecretNotFound
		}
		return "", ferr
	case stateOther:
		// Route through emitFallbackWarn so SetFallbackWarnWriter tests
		// can suppress the noise — identical discipline to Set's
		// stateOther branch. Dumping directly to os.Stderr here used to
		// bypass the test hook entirely. We still surface the underlying
		// keyring error (via the shared helper's message) so diagnostics
		// don't swallow a broken backend silently.
		emitFallbackWarn(p.SecretFilePath(prof), err)
		v, ferr := fileGet(p, prof, key)
		if ferr == nil {
			return v, nil
		}
		if errors.Is(ferr, ErrSecretNotFound) {
			return "", ErrSecretNotFound
		}
		return "", ferr
	}
	return "", fmt.Errorf("unreachable: classifyKeyringErr returned unknown state")
}

// Delete removes key from both backends. Idempotent: missing in either or
// both is not an error. Returns a non-nil error only when an actual I/O
// or keychain failure occurred (e.g., permission denied on the file).
func Delete(p paths.Paths, prof, key string) error {
	if err := validateNames(prof, key); err != nil {
		return err
	}

	// Keychain removal: swallow NotFound, surface everything else.
	kerr := keyring.Delete(service, account(prof, key))
	switch classifyKeyringErr(kerr) {
	case stateOK, stateNotFound, stateUnavailable:
		// stateUnavailable is fine for Delete — nothing to remove.
	case stateLocked:
		return ErrKeychainLocked
	case stateOther:
		return fmt.Errorf("keychain delete: %w", kerr)
	}

	// Always scrub the index (covers the case where keychain Set succeeded
	// previously but the backend is now unavailable — we still want the
	// stale index entry gone so List doesn't hallucinate a missing key).
	if err := indexRemove(p, prof, key); err != nil {
		return err
	}

	// File-store removal: swallow missing-file and missing-key.
	if err := fileDelete(p, prof, key); err != nil {
		return err
	}
	return nil
}

// List returns the keys stored for prof, merged across keychain (via the
// index file) and file store, sorted alphabetically with duplicates
// collapsed.
func List(p paths.Paths, prof string) ([]string, error) {
	if err := profile.ValidateName(prof); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}

	idx, err := indexLoad(p, prof)
	if err != nil {
		return nil, err
	}
	for _, k := range idx {
		seen[k] = struct{}{}
	}

	fm, err := fileLoad(p, prof)
	if err != nil {
		return nil, err
	}
	for k := range fm {
		seen[k] = struct{}{}
	}

	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// ---------- file store ----------

// fileLoad reads <SecretsDir>/<profile>.json. Returns an empty map if the
// file does not exist (a newly-provisioned profile has no fallback entries).
func fileLoad(p paths.Paths, prof string) (map[string]string, error) {
	path := p.SecretFilePath(prof)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(b) == 0 {
		return map[string]string{}, nil
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}

// fileSave writes m to <SecretsDir>/<profile>.json atomically via a
// sibling temp file + rename(2), with 0600 mode. Mirrors manifest.Save's
// discipline. If m is empty, removes the file instead of writing "{}" —
// keeps the on-disk state minimal and makes "no fallback entries" a
// file-not-present invariant that tests can assert against.
func fileSave(p paths.Paths, prof string, m map[string]string) error {
	path := p.SecretFilePath(prof)
	if len(m) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		return nil
	}
	if err := p.Ensure(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".secret-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Clean up the temp file if anything below fails; harmless no-op once
	// the rename consumes it.
	defer func() { _ = os.Remove(tmpName) }()

	// 0600 before any bytes hit disk. Defense in depth against umask
	// surprises; the plan calls out 0600 explicitly.
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("marshal secrets: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
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

func fileSet(p paths.Paths, prof, key, value string) error {
	m, err := fileLoad(p, prof)
	if err != nil {
		return err
	}
	m[key] = value
	return fileSave(p, prof, m)
}

func fileGet(p paths.Paths, prof, key string) (string, error) {
	m, err := fileLoad(p, prof)
	if err != nil {
		return "", err
	}
	v, ok := m[key]
	if !ok {
		return "", ErrSecretNotFound
	}
	return v, nil
}

func fileDelete(p paths.Paths, prof, key string) error {
	m, err := fileLoad(p, prof)
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return nil
	}
	delete(m, key)
	return fileSave(p, prof, m)
}

// ---------- keychain index (for List) ----------

// indexPath returns the sibling file recording which keys were written to
// the keychain for prof. Lives next to the fallback file so a `rm -rf
// secrets/` wipe resets both stores symmetrically.
func indexPath(p paths.Paths, prof string) string {
	return filepath.Join(p.SecretsDir, prof+".keychain-index.json")
}

// indexLoad returns the sorted list of keychain keys recorded for prof,
// or an empty slice if the index file does not yet exist. Malformed JSON
// is an error (rather than silently dropping the file) so users notice
// when the on-disk state is corrupt.
func indexLoad(p paths.Paths, prof string) ([]string, error) {
	path := indexPath(p, prof)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(b) == 0 {
		return nil, nil
	}
	var keys []string
	if err := json.Unmarshal(b, &keys); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return keys, nil
}

// indexSave writes the de-duplicated, sorted slice of keys to the index
// file atomically. Empty slice removes the file (mirrors fileSave).
func indexSave(p paths.Paths, prof string, keys []string) error {
	path := indexPath(p, prof)
	// De-duplicate + sort for stable on-disk output regardless of caller
	// order.
	seen := map[string]struct{}{}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	sort.Strings(out)

	if len(out) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		return nil
	}
	if err := p.Ensure(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".keychain-index-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("marshal index: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
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

func indexAdd(p paths.Paths, prof, key string) error {
	keys, err := indexLoad(p, prof)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if k == key {
			return nil // already present
		}
	}
	keys = append(keys, key)
	return indexSave(p, prof, keys)
}

func indexRemove(p paths.Paths, prof, key string) error {
	keys, err := indexLoad(p, prof)
	if err != nil {
		return err
	}
	out := keys[:0]
	found := false
	for _, k := range keys {
		if k == key {
			found = true
			continue
		}
		out = append(out, k)
	}
	if !found {
		return nil
	}
	return indexSave(p, prof, out)
}
