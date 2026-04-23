//go:build !windows

package cli

// TEST INVARIANT — DO NOT REMOVE:
//
// go-keyring's MockInit / MockInitWithError mutate package-level state.
// Tests in this file MUST NOT call t.Parallel() — neither on the top-level
// test nor on any subtest. Breaking that invariant introduces cross-test
// races that are hard to reproduce. Mirrors the same rule at the top of
// internal/secret/secret_test.go.

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dalley/ccp/internal/secret"
	"github.com/zalando/go-keyring"
)

// setupSecretCLI combines setupCLI with a keyring MockInit and a quiet
// fallback-warn writer. Every test in this file calls it. Returns the ccp
// root for tests that need to poke at the filesystem directly.
func setupSecretCLI(t *testing.T) string {
	t.Helper()
	root := setupCLI(t)
	keyring.MockInit()
	secret.SetFallbackWarnWriter(io.Discard)
	t.Cleanup(func() {
		// Restore the default writer so a test that forgets setup doesn't
		// write into this (freed) writer.
		secret.SetFallbackWarnWriter(os.Stderr)
	})
	return root
}

// ---------- happy paths ----------

func TestSecretSetGetRoundTrip(t *testing.T) {
	setupSecretCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := runCLI(t, "", "secret", "set", "work", "API_KEY", "s3cret"); err != nil {
		t.Fatalf("set: %v", err)
	}
	out, _, err := runCLI(t, "", "secret", "get", "work", "API_KEY")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Exactly the value, no trailing newline.
	if out != "s3cret" {
		t.Errorf("get stdout = %q, want %q", out, "s3cret")
	}
}

func TestSecretSetValueFlag(t *testing.T) {
	setupSecretCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "secret", "set", "work", "TOKEN", "--value=abc"); err != nil {
		t.Fatalf("set --value: %v", err)
	}
	out, _, err := runCLI(t, "", "secret", "get", "work", "TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if out != "abc" {
		t.Errorf("got %q, want abc", out)
	}
}

func TestSecretSetValueFlagAllowsEmptyString(t *testing.T) {
	// A caller explicitly passing --value="" should successfully store an
	// empty value — not get rejected as "no source". The Flag.Changed
	// branch of resolveSecretValue covers this.
	setupSecretCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "secret", "set", "work", "EMPTY_OK", "--value="); err != nil {
		t.Fatalf("set --value=\"\": %v", err)
	}
	out, _, err := runCLI(t, "", "secret", "get", "work", "EMPTY_OK")
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("expected empty string, got %q", out)
	}
}

func TestSecretSetStdin(t *testing.T) {
	setupSecretCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	// runCLI's second arg becomes the cobra command's stdin.
	if _, _, err := runCLI(t, "piped-value\n", "secret", "set", "work", "FOO", "--stdin"); err != nil {
		t.Fatalf("set --stdin: %v", err)
	}
	out, _, err := runCLI(t, "", "secret", "get", "work", "FOO")
	if err != nil {
		t.Fatal(err)
	}
	// Trailing '\n' should be stripped.
	if out != "piped-value" {
		t.Errorf("got %q, want %q", out, "piped-value")
	}
}

func TestSecretSetStdinStripsOnlyOneTrailingNewline(t *testing.T) {
	setupSecretCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	// Two trailing newlines — we strip exactly one.
	if _, _, err := runCLI(t, "multi\n\n", "secret", "set", "work", "K", "--stdin"); err != nil {
		t.Fatalf("set: %v", err)
	}
	out, _, err := runCLI(t, "", "secret", "get", "work", "K")
	if err != nil {
		t.Fatal(err)
	}
	if out != "multi\n" {
		t.Errorf("got %q, want %q", out, "multi\n")
	}
}

func TestSecretListPlain(t *testing.T) {
	setupSecretCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "secret", "set", "work", "B", "bval"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "secret", "set", "work", "A", "aval"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "secret", "list", "work")
	if err != nil {
		t.Fatal(err)
	}
	// secret.List sorts alphabetically.
	want := "A\nB\n"
	if out != want {
		t.Errorf("plain list = %q, want %q", out, want)
	}
}

func TestSecretListJSON(t *testing.T) {
	setupSecretCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "secret", "set", "work", "KEY1", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "secret", "set", "work", "KEY2", "v2"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "secret", "list", "work", "--json")
	if err != nil {
		t.Fatal(err)
	}
	// Parse rather than string-match — the contract is "parseable JSON of
	// this shape" and pinning to exact whitespace is brittle.
	var got struct {
		Profile string   `json:"profile"`
		Keys    []string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, out)
	}
	if got.Profile != "work" {
		t.Errorf("profile = %q, want work", got.Profile)
	}
	if len(got.Keys) != 2 || got.Keys[0] != "KEY1" || got.Keys[1] != "KEY2" {
		t.Errorf("keys = %v, want [KEY1 KEY2]", got.Keys)
	}
}

func TestSecretListJSONEmptyReturnsArrayNotNull(t *testing.T) {
	// Edge case callers rely on: an empty list must serialize as "keys":[],
	// not "keys":null. Pinned because downstream JSON consumers often
	// typecheck (Array.isArray, jq's type="array") before iterating.
	setupSecretCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "secret", "list", "work", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"keys": []`) {
		t.Errorf("expected empty array, got:\n%s", out)
	}
}

func TestSecretRmIdempotent(t *testing.T) {
	setupSecretCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "secret", "set", "work", "DELME", "tmp"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "secret", "rm", "work", "DELME"); err != nil {
		t.Fatalf("rm existing: %v", err)
	}
	// Second rm on the same key should succeed silently.
	if _, _, err := runCLI(t, "", "secret", "rm", "work", "DELME"); err != nil {
		t.Fatalf("rm missing (should be idempotent): %v", err)
	}
	// Now get should fail with ExitUser (ErrSecretNotFound).
	_, _, err := runCLI(t, "", "secret", "get", "work", "DELME")
	if err == nil {
		t.Fatal("expected get-after-rm to fail")
	}
	if ExitCodeFor(err) != ExitUser {
		t.Errorf("exit = %d, want %d (user)", ExitCodeFor(err), ExitUser)
	}
}

// ---------- edge / error cases ----------

func TestSecretSetNonTTYWithoutSourceRefuses(t *testing.T) {
	// In tests, stdin is not a TTY (runCLI supplies a reader). With no
	// positional, no --value, no --stdin, we must refuse with a message
	// naming all three sources. Mirrors the profile_delete.go discipline.
	setupSecretCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCLI(t, "", "secret", "set", "work", "KEY")
	if err == nil {
		t.Fatal("expected refusal when no value source on non-TTY stdin")
	}
	msg := err.Error()
	for _, hint := range []string{"--value", "--stdin", "positional"} {
		if !strings.Contains(msg, hint) {
			t.Errorf("expected error to mention %q, got: %s", hint, msg)
		}
	}
}

func TestSecretSetInvalidProfileName(t *testing.T) {
	setupSecretCLI(t)
	_, _, err := runCLI(t, "", "secret", "set", "BAD NAME", "K", "v")
	if err == nil {
		t.Fatal("expected invalid profile name to fail")
	}
	if ExitCodeFor(err) != ExitUser {
		t.Errorf("exit = %d, want %d", ExitCodeFor(err), ExitUser)
	}
}

func TestSecretSetInvalidKey(t *testing.T) {
	setupSecretCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCLI(t, "", "secret", "set", "work", "KEY WITH SPACE", "v")
	if err == nil {
		t.Fatal("expected invalid key to fail")
	}
	// The error message should come from secret.ValidateKey — assert that
	// the grammar hint is present so a caller knows WHY.
	if !strings.Contains(err.Error(), "secret key") {
		t.Errorf("expected validation message; got: %v", err)
	}
}

func TestSecretGetMissingReturnsExitUser(t *testing.T) {
	setupSecretCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCLI(t, "", "secret", "get", "work", "NEVER_SET")
	if err == nil {
		t.Fatal("expected get on missing key to fail")
	}
	if !errors.Is(err, secret.ErrSecretNotFound) {
		t.Errorf("expected ErrSecretNotFound, got %v", err)
	}
	if ExitCodeFor(err) != ExitUser {
		t.Errorf("exit = %d, want %d (user)", ExitCodeFor(err), ExitUser)
	}
}

// ---------- integration: end-to-end with ccp exec ----------

// TestSecretSetThenExecResolvesKeychainRef closes the loop between Unit 5
// (CLI) and Unit 4 (render integration in exec): a profile with a
// `{{ keychain:KEY }}` reference in settings.json should render to the
// stored value after `ccp secret set`. Uses the MockInit keychain backend,
// not the real one.
func TestSecretSetThenExecResolvesKeychainRef(t *testing.T) {
	root := setupSecretCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Put a keychain ref into the source settings.json. The render path in
	// exec refreshes the runtime-side file before spawning the child.
	src := filepath.Join(root, ".config/ccp/profiles/work/settings.json")
	if err := os.WriteFile(src, []byte(`{"token":"{{ keychain:TOKEN }}"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "secret", "set", "work", "TOKEN", "val-42"); err != nil {
		t.Fatalf("secret set: %v", err)
	}

	runtimeFile := filepath.Join(root, ".claude-work/settings.json")
	out, _, err := runCLI(t, "", "exec", "work", "--", "/bin/cat", runtimeFile)
	if err != nil {
		t.Fatalf("exec: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"token":"val-42"`) {
		t.Errorf("expected rendered value, got: %s", out)
	}
	if strings.Contains(out, "{{") {
		t.Errorf("ref not rendered: %s", out)
	}
}

// ---------- integration: concurrent set under fallback-file contention ----------

// TestSecretSetConcurrentUnderUnavailableKeychain drives N goroutines
// through `secret set` while the keychain backend is unavailable, forcing
// every Set down the fallback-file path. The CLI's withLockedState is what
// serializes them — without the global ccp flock, two goroutines would
// race on fileLoad+fileSave and clobber entries.
//
// Verifies that all N keys are present in the final list (no silent
// clobbering) and that the fallback-file JSON is readable (no partial writes).
func TestSecretSetConcurrentUnderUnavailableKeychain(t *testing.T) {
	setupCLI(t) // NOTE: bypass MockInit so MockInitWithError can swap in.
	keyring.MockInitWithError(errors.New("dbus: no such interface"))
	secret.SetFallbackWarnWriter(io.Discard)
	t.Cleanup(func() {
		// Restore so later tests using setupSecretCLI don't get error-mode.
		keyring.MockInit()
		secret.SetFallbackWarnWriter(os.Stderr)
	})

	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatalf("create: %v", err)
	}

	const N = 8
	var wg sync.WaitGroup
	errCh := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "KEY_" + string(rune('A'+i))
			_, _, err := runCLI(t, "", "secret", "set", "work", key, "v")
			if err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent set failed: %v", err)
	}

	// All N keys must be present. If two goroutines raced on
	// fileLoad+fileSave, we'd lose writes here.
	out, _, err := runCLI(t, "", "secret", "list", "work", "--json")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if len(got.Keys) != N {
		t.Errorf("concurrent set lost keys: got %d, want %d; keys=%v", len(got.Keys), N, got.Keys)
	}
}
