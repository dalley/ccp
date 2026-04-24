//go:build !windows

package refs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// -----------------------------------------------------------------------------
// Grammar
// -----------------------------------------------------------------------------
//
//   ref      := "{{" ws* scheme ws* "}}"
//   scheme   := "keychain:" key
//             | "op://"      path
//             | "env."       name
//   escape   := "{{!}}"    (immediately preceding a ref; strips both tokens
//                           from output and emits the following ref literally)
//
// HasRefs matches only scheme signatures via regex; a bare `{{` without a
// recognized scheme is not a ref and passes through verbatim. This is
// load-bearing to avoid collisions with Helm-style templates and markdown
// prose that happens to contain `{{ something }}`.

// hasRefsRe matches a scheme signature, tolerating whitespace after the
// opening `{{`. We deliberately do NOT match bare `{{`.
var hasRefsRe = regexp.MustCompile(`\{\{\s*(keychain:|op://|env\.)`)

// escapeMarker is the literal escape token emitted by users who want a ref
// to render verbatim. It MUST appear immediately before the `{{ ... }}` it
// escapes — no intervening bytes.
const escapeMarker = "{{!}}"

// HasRefs reports whether b contains any recognized reference scheme.
// Bare `{{` without a known scheme (Helm, prose) is NOT a ref.
func HasRefs(b []byte) bool {
	return hasRefsRe.Match(b)
}

// HasAnyRefs walks dir and returns true on the first regular file whose
// contents contain a ref. Used by `ccp exec` to decide whether a profile
// requires secret resolution before launching.
//
// Unreadable files (permission denied, transient I/O) are skipped rather
// than aborting the walk — a file we can't read isn't ref-bearing for
// our purposes, and blocking on one unreadable leaf would make the whole
// scan a hostage to noise in the tree. The outer walk error path still
// fires on directory-level failures (bad dir, corrupt fs) which are
// real scan failures.
func HasAnyRefs(dir string) (bool, error) {
	found := false
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			// Unreadable leaf: skip, keep walking. Not ref-bearing as
			// far as this scan can see.
			return nil
		}
		if HasRefs(b) {
			found = true
			return fs.SkipAll // early exit
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return found, walkErr
	}
	return found, nil
}

// -----------------------------------------------------------------------------
// Ref tagged union
// -----------------------------------------------------------------------------

// Ref is the parsed form of a single template reference.
type Ref interface{ isRef() }

// RefKeychain resolves against the current profile's keychain entry.
// service="ccp", account=<Resolver.Profile>, key=<Key>.
type RefKeychain struct{ Key string }

// RefOp shells out to `op read <Ref>` — Ref is the full op://... string.
type RefOp struct{ Ref string }

// RefEnv resolves via os.LookupEnv(Var).
type RefEnv struct{ Var string }

func (RefKeychain) isRef() {}
func (RefOp) isRef()       {}
func (RefEnv) isRef()      {}

// ParseRef parses the inner content between `{{` and `}}` (already trimmed
// of surrounding whitespace) and returns the matching Ref.
//
// Returns an error for:
//   - empty key/path/var inside a recognized scheme (malformed)
//   - strings that don't match any recognized scheme
func ParseRef(s string) (Ref, error) {
	switch {
	case strings.HasPrefix(s, "keychain:"):
		key := s[len("keychain:"):]
		if key == "" {
			return nil, fmt.Errorf("empty keychain key in ref")
		}
		return RefKeychain{Key: key}, nil
	case strings.HasPrefix(s, "op://"):
		// The op CLI accepts op://Vault/Item/field; we don't validate the
		// path shape here — `op read` will reject malformed refs. We only
		// reject the "no path at all" case.
		if s == "op://" {
			return nil, fmt.Errorf("empty op path in ref")
		}
		return RefOp{Ref: s}, nil
	case strings.HasPrefix(s, "env."):
		name := s[len("env."):]
		if name == "" {
			return nil, fmt.Errorf("empty env var name in ref")
		}
		return RefEnv{Var: name}, nil
	default:
		return nil, fmt.Errorf("unrecognized ref scheme in %q", s)
	}
}

// -----------------------------------------------------------------------------
// Resolver
// -----------------------------------------------------------------------------

// Resolver produces the plaintext value for a parsed Ref. Callers
// control timeout policy via ctx; the resolver must not set its own.
type Resolver interface {
	Resolve(ctx context.Context, ref Ref) (string, error)
}

// DefaultResolver resolves refs against a profile-scoped keychain, the
// system `op` CLI, and process env. Fields are injectable for testing;
// zero-value production use works out of the box.
type DefaultResolver struct {
	// Profile scopes keychain lookups. Required for RefKeychain to work.
	Profile string

	// KeyringGet, if non-nil, replaces the default keychain lookup. This
	// lets tests avoid a real keyring; Unit 3 will supply a production
	// implementation backed by zalando/go-keyring.
	KeyringGet func(service, account, key string) (string, error)

	// EnvLookup, if non-nil, replaces os.LookupEnv. Injectable so
	// integration tests don't have to mutate process env.
	EnvLookup func(name string) (string, bool)
}

// Resolve dispatches on the ref type. Errors are always wrapped with
// ErrSecretRefUnresolved so callers can use errors.Is for exit-code
// classification.
func (r DefaultResolver) Resolve(ctx context.Context, ref Ref) (string, error) {
	switch x := ref.(type) {
	case RefKeychain:
		return r.resolveKeychain(ctx, x)
	case RefEnv:
		return r.resolveEnv(x)
	case RefOp:
		return r.resolveOp(ctx, x)
	default:
		return "", fmt.Errorf("refs: unknown ref type %T: %w", ref, ErrSecretRefUnresolved)
	}
}

func (r DefaultResolver) resolveKeychain(ctx context.Context, ref RefKeychain) (string, error) {
	if r.KeyringGet == nil {
		// Unit 3 will wire the production go-keyring path. Until then,
		// Render callers see a clean "unresolved" error rather than a
		// crash — letting Unit 2 ship independently.
		return "", fmt.Errorf("refs: keychain lookup unavailable for %q (Unit 3 wires go-keyring): %w",
			ref.Key, ErrSecretRefUnresolved)
	}
	if r.Profile == "" {
		return "", fmt.Errorf("refs: keychain ref %q requires a resolver profile: %w",
			ref.Key, ErrSecretRefUnresolved)
	}
	v, err := r.KeyringGet("ccp", r.Profile, ref.Key)
	if err != nil {
		return "", fmt.Errorf("refs: keychain lookup %q failed: %v: %w",
			ref.Key, err, ErrSecretRefUnresolved)
	}
	return v, nil
}

func (r DefaultResolver) resolveEnv(ref RefEnv) (string, error) {
	lookup := r.EnvLookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	v, ok := lookup(ref.Var)
	if !ok {
		return "", fmt.Errorf("refs: env var %q not set: %w", ref.Var, ErrSecretRefUnresolved)
	}
	return v, nil
}

// -----------------------------------------------------------------------------
// op shell-out
// -----------------------------------------------------------------------------

// opRead is the package-level execution hook for `op read <ref>`. Tests
// override this to avoid a real `op` binary. Mirrors the warnWriter
// pattern in internal/sync/auth.go.
var opRead = defaultOpRead

// isTTY probes stdin — `op` may prompt on an attached TTY, which is a
// refused state without OP_SERVICE_ACCOUNT_TOKEN. Injectable for tests.
var isTTY = func() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// defaultOpRead runs `op read <ref>` honoring the caller-supplied context.
// The context carries the timeout — this function does not impose one.
func defaultOpRead(ctx context.Context, ref string) (string, error) {
	cmd := exec.CommandContext(ctx, "op", "read", ref)
	// Inherit env so OP_SERVICE_ACCOUNT_TOKEN, OP_ACCOUNT, etc. flow to op.
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Surface stderr in the error message — op's diagnostics are useful.
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", err
		}
		return "", fmt.Errorf("%v: %s", err, msg)
	}
	return stdout.String(), nil
}

// SetOpRead lets tests inject a fake op reader. Prefer in-package test
// override where possible; this setter exists for callers in other
// packages that need to short-circuit op during integration tests.
func SetOpRead(fn func(ctx context.Context, ref string) (string, error)) {
	if fn == nil {
		opRead = defaultOpRead
		return
	}
	opRead = fn
}

func (r DefaultResolver) resolveOp(ctx context.Context, ref RefOp) (string, error) {
	// Guard against interactive prompts in non-TTY contexts. Without a
	// service-account token, `op read` may block forever waiting for
	// biometric/master-password input — refuse up front with a hint.
	if !isTTY() && os.Getenv("OP_SERVICE_ACCOUNT_TOKEN") == "" {
		return "", fmt.Errorf(
			"refs: cannot resolve %s: op may prompt interactively; "+
				"set OP_SERVICE_ACCOUNT_TOKEN for non-interactive use: %w",
			ref.Ref, ErrSecretRefUnresolved)
	}
	out, err := opRead(ctx, ref.Ref)
	if err != nil {
		return "", fmt.Errorf("refs: op read %s failed: %v: %w",
			ref.Ref, err, ErrSecretRefUnresolved)
	}
	// `op read` prints a trailing newline — strip exactly one so rendered
	// output doesn't end up with a stray newline inside a JSON string.
	return strings.TrimRight(out, "\r\n"), nil
}

// -----------------------------------------------------------------------------
// Render
// -----------------------------------------------------------------------------

// Render scans b for refs and resolves each via r. Short-circuits when
// HasRefs is false, returning the input unchanged. Malformed refs inside
// a recognized scheme yield a parse error naming the byte offset.
//
// The escape sequence `{{!}}` immediately preceding a ref-shaped token
// emits the following `{{ ... }}` literally and strips the escape.
func Render(ctx context.Context, b []byte, r Resolver) ([]byte, error) {
	if !HasRefs(b) {
		return b, nil
	}

	var out bytes.Buffer
	out.Grow(len(b))

	i := 0
	for i < len(b) {
		// Look for the next `{{` or escape.
		j := bytes.Index(b[i:], []byte("{{"))
		if j < 0 {
			out.Write(b[i:])
			break
		}
		// Flush bytes up to the candidate opener.
		out.Write(b[i : i+j])
		start := i + j

		// Escape sequence: `{{!}}` followed immediately by a ref.
		if bytes.HasPrefix(b[start:], []byte(escapeMarker)) {
			afterEscape := start + len(escapeMarker)
			// The escape strips itself. The following ref must be
			// immediately adjacent (no whitespace) AND be a real ref
			// signature — otherwise the escape is a no-op decoration.
			if afterEscape < len(b) && HasRefs(b[afterEscape:afterEscape+minInt(len(b)-afterEscape, 64)]) &&
				bytes.HasPrefix(b[afterEscape:], []byte("{{")) {
				// Find the matching `}}` and emit the whole span verbatim.
				close := bytes.Index(b[afterEscape+2:], []byte("}}"))
				if close >= 0 {
					end := afterEscape + 2 + close + 2
					out.Write(b[afterEscape:end])
					i = end
					continue
				}
			}
			// Escape with no adjacent ref — emit the escape marker verbatim.
			out.WriteString(escapeMarker)
			i = afterEscape
			continue
		}

		// Candidate ref: scan scheme signature before committing.
		if !hasRefsRe.Match(b[start:minInt(start+64, len(b))]) ||
			!hasRefsRe.MatchString(peekOpener(b[start:])) {
			// Bare `{{` with no recognized scheme: emit `{{` and advance.
			out.WriteString("{{")
			i = start + 2
			continue
		}

		// Find the closing `}}`.
		close := bytes.Index(b[start+2:], []byte("}}"))
		if close < 0 {
			// Unclosed `{{`: pass through verbatim (per plan).
			out.Write(b[start:])
			break
		}
		end := start + 2 + close + 2
		inner := strings.TrimSpace(string(b[start+2 : start+2+close]))

		ref, err := ParseRef(inner)
		if err != nil {
			return nil, fmt.Errorf("refs: malformed ref at byte offset %d: %v (ref was %q)",
				start, err, string(b[start:end]))
		}
		val, err := r.Resolve(ctx, ref)
		if err != nil {
			return nil, err
		}
		out.WriteString(val)
		i = end
	}
	return out.Bytes(), nil
}

// peekOpener returns the span `{{ ... }}` (or the first 64 bytes of it)
// so hasRefsRe can match only against the candidate opener, not a tail
// that may happen to contain another ref later on.
func peekOpener(b []byte) string {
	if len(b) < 2 || b[0] != '{' || b[1] != '{' {
		return ""
	}
	end := bytes.Index(b[2:], []byte("}}"))
	if end < 0 {
		return string(b[:minInt(len(b), 64)])
	}
	return string(b[:2+end+2])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
