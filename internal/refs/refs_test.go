//go:build !windows

package refs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- ParseRef ------------------------------------------------------------

func TestParseRef(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Ref
		wantErr bool
	}{
		{"keychain simple", "keychain:API_KEY", RefKeychain{Key: "API_KEY"}, false},
		{"keychain with dash", "keychain:my-key_1", RefKeychain{Key: "my-key_1"}, false},
		{"op full path", "op://Vault/Item/field", RefOp{Ref: "op://Vault/Item/field"}, false},
		{"env var", "env.FOO", RefEnv{Var: "FOO"}, false},
		{"env var with underscore", "env.FOO_BAR_BAZ", RefEnv{Var: "FOO_BAR_BAZ"}, false},
		{"keychain empty key", "keychain:", nil, true},
		{"op empty", "op://", nil, true},
		{"env empty", "env.", nil, true},
		{"unknown scheme", "unknown:foo", nil, true},
		{"bare text", "not a ref", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRef(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseRef(%q) = %v, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRef(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ParseRef(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// --- HasRefs -------------------------------------------------------------

func TestHasRefs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"plain text", "hello world", false},
		{"bare opener", "just a {{ in the middle", false},
		{"helm template", "value: {{ .Values.foo }}", false},
		{"markdown prose", "Use `{{ placeholder }}` to indicate a variable", false},
		{"keychain ref", `{"api": "{{ keychain:API_KEY }}"}`, true},
		{"op ref", `token={{ op://Vault/Item/field }}`, true},
		{"env ref", "home={{ env.HOME }}", true},
		{"tight whitespace", "{{keychain:KEY}}", true},
		{"loose whitespace", "{{   keychain:KEY   }}", true},
		{"escape sequence alone", "{{!}}{{ keychain:KEY }}", true}, // still contains a ref signature
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := HasRefs([]byte(tc.in))
			if got != tc.want {
				t.Errorf("HasRefs(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestHasRefsCheapOnLargeNonRefInput(t *testing.T) {
	// Regression guard: scanning a 100KB blob without refs must finish fast.
	var sb strings.Builder
	for i := 0; i < 100_000; i++ {
		sb.WriteByte('a')
	}
	start := time.Now()
	if HasRefs([]byte(sb.String())) {
		t.Fatal("HasRefs returned true for non-ref blob")
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("HasRefs on 100KB took %v, want <50ms", d)
	}
}

// --- HasAnyRefs ----------------------------------------------------------

func TestHasAnyRefsTrue(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("no refs here"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hooks", "foo.sh"), []byte("#!/bin/sh\nTOKEN={{ keychain:TOKEN }}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := HasAnyRefs(dir)
	if err != nil {
		t.Fatalf("HasAnyRefs: %v", err)
	}
	if !ok {
		t.Fatal("HasAnyRefs = false, want true")
	}
}

func TestHasAnyRefsFalse(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"foo":"bar"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hooks", "foo.sh"), []byte("#!/bin/sh\necho hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := HasAnyRefs(dir)
	if err != nil {
		t.Fatalf("HasAnyRefs: %v", err)
	}
	if ok {
		t.Fatal("HasAnyRefs = true, want false")
	}
}

func TestHasAnyRefsSkipsNonexistent(t *testing.T) {
	ok, err := HasAnyRefs(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("HasAnyRefs on missing dir = nil error, want error")
	}
	if ok {
		t.Fatal("HasAnyRefs on missing dir = true")
	}
}

// TestHasAnyRefsSkipsUnreadableFileAndFindsLater plants a 0000-mode file
// under the scan root AND a later ref-bearing file; the scan must skip
// the unreadable leaf (permission denied) rather than aborting, and
// still find the real ref in the sibling. Regression guard for the fix
// where walkFn used to `return err` from ReadFile and stop the whole
// walk on the first unreadable file.
func TestHasAnyRefsSkipsUnreadableFileAndFindsLater(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file perms; skip")
	}
	dir := t.TempDir()
	unreadable := filepath.Join(dir, "00-unreadable.txt")
	if err := os.WriteFile(unreadable, []byte("whatever"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })

	// "99-" so lexical ordering puts it after the unreadable — WalkDir
	// is deterministic in sort order, so this confirms we kept going
	// past the failure.
	hasRef := filepath.Join(dir, "99-real.sh")
	if err := os.WriteFile(hasRef, []byte("TOKEN={{ keychain:TOKEN }}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ok, err := HasAnyRefs(dir)
	if err != nil {
		t.Fatalf("HasAnyRefs unexpectedly errored: %v", err)
	}
	if !ok {
		t.Fatal("HasAnyRefs = false despite a ref-bearing sibling next to an unreadable file")
	}
}

// --- Render: happy paths -------------------------------------------------

type mapResolver struct {
	keychain map[string]string
	env      map[string]string
	op       map[string]string
}

func (r mapResolver) Resolve(ctx context.Context, ref Ref) (string, error) {
	switch x := ref.(type) {
	case RefKeychain:
		if v, ok := r.keychain[x.Key]; ok {
			return v, nil
		}
		return "", fmt.Errorf("keychain key %q not found: %w", x.Key, ErrSecretRefUnresolved)
	case RefEnv:
		if v, ok := r.env[x.Var]; ok {
			return v, nil
		}
		return "", fmt.Errorf("env var %q not set: %w", x.Var, ErrSecretRefUnresolved)
	case RefOp:
		if v, ok := r.op[x.Ref]; ok {
			return v, nil
		}
		return "", fmt.Errorf("op ref %q not resolvable: %w", x.Ref, ErrSecretRefUnresolved)
	}
	return "", fmt.Errorf("unknown ref type: %w", ErrSecretRefUnresolved)
}

func TestRenderSingleKeychainRef(t *testing.T) {
	r := mapResolver{keychain: map[string]string{"API_KEY": "s3cret"}}
	in := []byte(`{"api":"{{ keychain:API_KEY }}"}`)
	out, err := Render(context.Background(), in, r)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(out) != `{"api":"s3cret"}` {
		t.Errorf("Render = %q", out)
	}
}

func TestRenderMultipleSchemes(t *testing.T) {
	r := mapResolver{
		keychain: map[string]string{"K": "kv"},
		env:      map[string]string{"E": "ev"},
		op:       map[string]string{"op://v/i/f": "ov"},
	}
	in := []byte("k={{ keychain:K }} e={{ env.E }} o={{ op://v/i/f }}\n")
	out, err := Render(context.Background(), in, r)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(out) != "k=kv e=ev o=ov\n" {
		t.Errorf("Render = %q", out)
	}
}

func TestRenderWhitespaceVariations(t *testing.T) {
	r := mapResolver{keychain: map[string]string{"K": "v"}}
	cases := []string{
		"{{keychain:K}}",
		"{{ keychain:K }}",
		"{{   keychain:K   }}",
		"{{\tkeychain:K\t}}",
	}
	for _, c := range cases {
		out, err := Render(context.Background(), []byte(c), r)
		if err != nil {
			t.Errorf("Render(%q): %v", c, err)
			continue
		}
		if string(out) != "v" {
			t.Errorf("Render(%q) = %q, want %q", c, out, "v")
		}
	}
}

// --- Render: pass-through ------------------------------------------------

func TestRenderBareOpenerPassesThrough(t *testing.T) {
	r := mapResolver{}
	in := []byte("this has {{ but no close and no ref scheme")
	out, err := Render(context.Background(), in, r)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("Render = %q, want verbatim %q", out, in)
	}
}

func TestRenderHelmSyntaxPassesThrough(t *testing.T) {
	r := mapResolver{}
	in := []byte("value: {{ .Values.foo }}\nkey: {{ .Chart.Name }}")
	out, err := Render(context.Background(), in, r)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("Render = %q, want verbatim", out)
	}
}

func TestRenderMarkdownProsePassesThrough(t *testing.T) {
	r := mapResolver{}
	in := []byte("Use `{{ placeholder }}` for a variable")
	out, err := Render(context.Background(), in, r)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("Render = %q, want verbatim", out)
	}
}

// --- Render: escape sequence --------------------------------------------

func TestRenderEscapeSequenceEmitsLiteral(t *testing.T) {
	r := mapResolver{keychain: map[string]string{"K": "SHOULD-NOT-APPEAR"}}
	in := []byte("{{!}}{{ keychain:K }}")
	out, err := Render(context.Background(), in, r)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// {{!}} is stripped; the following {{ keychain:K }} becomes literal.
	want := "{{ keychain:K }}"
	if string(out) != want {
		t.Errorf("Render = %q, want %q", out, want)
	}
}

func TestRenderEscapeSequenceOnlyAffectsImmediateNext(t *testing.T) {
	r := mapResolver{keychain: map[string]string{"K": "resolved"}}
	in := []byte("{{!}}{{ keychain:K }} and {{ keychain:K }}")
	out, err := Render(context.Background(), in, r)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "{{ keychain:K }} and resolved"
	if string(out) != want {
		t.Errorf("Render = %q, want %q", out, want)
	}
}

func TestRenderEscapeRequiresImmediateAdjacency(t *testing.T) {
	// `{{!}}` with whitespace before the ref is NOT an escape — escape must
	// be immediately adjacent.
	r := mapResolver{keychain: map[string]string{"K": "resolved"}}
	in := []byte("{{!}} {{ keychain:K }}")
	out, err := Render(context.Background(), in, r)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "{{!}} resolved"
	if string(out) != want {
		t.Errorf("Render = %q, want %q", out, want)
	}
}

// --- Render: short-circuit -----------------------------------------------

type panickyResolver struct{}

func (panickyResolver) Resolve(ctx context.Context, ref Ref) (string, error) {
	panic("resolver must not be called when HasRefs is false")
}

func TestRenderNoRefsSkipsResolver(t *testing.T) {
	in := []byte(`{"just":"plain json","no":"refs here"}`)
	out, err := Render(context.Background(), in, panickyResolver{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("Render = %q, want verbatim", out)
	}
}

// --- Render: errors ------------------------------------------------------

func TestRenderMalformedKeychainError(t *testing.T) {
	in := []byte("before {{ keychain: }} after")
	_, err := Render(context.Background(), in, mapResolver{})
	if err == nil {
		t.Fatal("Render on empty keychain key = nil, want error")
	}
	if !strings.Contains(err.Error(), "offset") {
		t.Errorf("error %q missing byte offset", err)
	}
	if !strings.Contains(err.Error(), "keychain") {
		t.Errorf("error %q does not mention keychain", err)
	}
}

func TestRenderMalformedOpError(t *testing.T) {
	in := []byte("x {{ op:// }} y")
	_, err := Render(context.Background(), in, mapResolver{})
	if err == nil {
		t.Fatal("Render on empty op = nil, want error")
	}
	if !strings.Contains(err.Error(), "offset") {
		t.Errorf("error %q missing offset", err)
	}
}

func TestRenderMalformedEnvError(t *testing.T) {
	in := []byte("{{ env. }}")
	_, err := Render(context.Background(), in, mapResolver{})
	if err == nil {
		t.Fatal("Render on empty env var = nil, want error")
	}
	if !strings.Contains(err.Error(), "offset") {
		t.Errorf("error %q missing offset", err)
	}
}

func TestRenderMissingKeychainEntryWrapsSentinel(t *testing.T) {
	in := []byte("{{ keychain:MISSING }}")
	_, err := Render(context.Background(), in, mapResolver{})
	if err == nil {
		t.Fatal("Render = nil error, want ErrSecretRefUnresolved")
	}
	if !errors.Is(err, ErrSecretRefUnresolved) {
		t.Errorf("err = %v, want wrapping ErrSecretRefUnresolved", err)
	}
	if !strings.Contains(err.Error(), "MISSING") {
		t.Errorf("err %q does not mention ref key", err)
	}
}

// --- DefaultResolver -----------------------------------------------------

func TestDefaultResolverEnv(t *testing.T) {
	t.Setenv("CCP_REFS_TEST_VAR", "hello")
	r := DefaultResolver{Profile: "work"}
	v, err := r.Resolve(context.Background(), RefEnv{Var: "CCP_REFS_TEST_VAR"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if v != "hello" {
		t.Errorf("Resolve = %q, want %q", v, "hello")
	}
}

func TestDefaultResolverEnvUnset(t *testing.T) {
	r := DefaultResolver{Profile: "work"}
	_, err := r.Resolve(context.Background(), RefEnv{Var: "CCP_REFS_TEST_VAR_UNSET_XYZ"})
	if err == nil {
		t.Fatal("Resolve on unset env = nil, want error")
	}
	if !errors.Is(err, ErrSecretRefUnresolved) {
		t.Errorf("err = %v, want wrapping ErrSecretRefUnresolved", err)
	}
}

func TestDefaultResolverKeychainStub(t *testing.T) {
	// Unit 3 wires the real keychain. For now the default resolver's
	// keychain path returns ErrSecretRefUnresolved so callers surface a
	// sensible error instead of panicking.
	r := DefaultResolver{Profile: "work"}
	_, err := r.Resolve(context.Background(), RefKeychain{Key: "ANY"})
	if err == nil {
		t.Fatal("Resolve keychain without injected lookup = nil, want error")
	}
	if !errors.Is(err, ErrSecretRefUnresolved) {
		t.Errorf("err = %v, want wrapping ErrSecretRefUnresolved", err)
	}
}

func TestDefaultResolverKeychainInjected(t *testing.T) {
	r := DefaultResolver{
		Profile: "work",
		KeyringGet: func(service, account, key string) (string, error) {
			if service != "ccp" {
				t.Errorf("service = %q, want ccp", service)
			}
			if account != "work" {
				t.Errorf("account = %q, want work", account)
			}
			if key != "API_KEY" {
				t.Errorf("key = %q, want API_KEY", key)
			}
			return "injected-value", nil
		},
	}
	v, err := r.Resolve(context.Background(), RefKeychain{Key: "API_KEY"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if v != "injected-value" {
		t.Errorf("Resolve = %q, want injected-value", v)
	}
}

func TestDefaultResolverKeychainInjectedNotFound(t *testing.T) {
	r := DefaultResolver{
		Profile: "work",
		KeyringGet: func(service, account, key string) (string, error) {
			return "", errors.New("secret not found in keyring")
		},
	}
	_, err := r.Resolve(context.Background(), RefKeychain{Key: "API_KEY"})
	if err == nil {
		t.Fatal("Resolve on injected-missing = nil, want error")
	}
	if !errors.Is(err, ErrSecretRefUnresolved) {
		t.Errorf("err = %v, want wrapping ErrSecretRefUnresolved", err)
	}
}

// --- DefaultResolver: op path (using fake opRead) ------------------------

// withOpRead swaps the package-level opRead for the duration of the test.
func withOpRead(t *testing.T, fn func(ctx context.Context, ref string) (string, error)) {
	t.Helper()
	prev := opRead
	opRead = fn
	t.Cleanup(func() { opRead = prev })
}

// withTTY overrides the isTTY probe for the duration of the test.
func withTTY(t *testing.T, tty bool) {
	t.Helper()
	prev := isTTY
	isTTY = func() bool { return tty }
	t.Cleanup(func() { isTTY = prev })
}

func TestResolveOpHappy(t *testing.T) {
	withTTY(t, true)
	withOpRead(t, func(ctx context.Context, ref string) (string, error) {
		if ref != "op://Vault/Item/field" {
			t.Errorf("opRead got %q", ref)
		}
		return "op-secret\n", nil
	})
	r := DefaultResolver{Profile: "work"}
	v, err := r.Resolve(context.Background(), RefOp{Ref: "op://Vault/Item/field"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// op CLI typically emits a trailing newline; we strip it.
	if v != "op-secret" {
		t.Errorf("Resolve = %q, want %q", v, "op-secret")
	}
}

func TestResolveOpNonTTYNoServiceAccountRefuses(t *testing.T) {
	withTTY(t, false)
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	called := false
	withOpRead(t, func(ctx context.Context, ref string) (string, error) {
		called = true
		return "should-not-be-called", nil
	})
	r := DefaultResolver{Profile: "work"}
	_, err := r.Resolve(context.Background(), RefOp{Ref: "op://V/I/F"})
	if err == nil {
		t.Fatal("Resolve non-TTY without service account = nil, want refuse")
	}
	if called {
		t.Error("opRead was called despite no service account + non-TTY")
	}
	if !errors.Is(err, ErrSecretRefUnresolved) {
		t.Errorf("err = %v, want wrapping ErrSecretRefUnresolved", err)
	}
	if !strings.Contains(err.Error(), "OP_SERVICE_ACCOUNT_TOKEN") {
		t.Errorf("err %q missing hint", err)
	}
}

func TestResolveOpNonTTYWithServiceAccountProceeds(t *testing.T) {
	withTTY(t, false)
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "ops_example")
	withOpRead(t, func(ctx context.Context, ref string) (string, error) {
		return "got-it", nil
	})
	r := DefaultResolver{Profile: "work"}
	v, err := r.Resolve(context.Background(), RefOp{Ref: "op://V/I/F"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if v != "got-it" {
		t.Errorf("Resolve = %q", v)
	}
}

func TestResolveOpContextCancelledWrapsSentinel(t *testing.T) {
	withTTY(t, true)
	withOpRead(t, func(ctx context.Context, ref string) (string, error) {
		return "", context.DeadlineExceeded
	})
	r := DefaultResolver{Profile: "work"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.Resolve(ctx, RefOp{Ref: "op://V/I/F"})
	if err == nil {
		t.Fatal("Resolve with cancelled ctx = nil, want error")
	}
	if !errors.Is(err, ErrSecretRefUnresolved) {
		t.Errorf("err = %v, want wrapping ErrSecretRefUnresolved", err)
	}
}

func TestResolveOpCommandErrorWrapsSentinel(t *testing.T) {
	withTTY(t, true)
	withOpRead(t, func(ctx context.Context, ref string) (string, error) {
		return "", errors.New("op exited 1: item not found")
	})
	r := DefaultResolver{Profile: "work"}
	_, err := r.Resolve(context.Background(), RefOp{Ref: "op://V/I/F"})
	if err == nil {
		t.Fatal("Resolve = nil, want error")
	}
	if !errors.Is(err, ErrSecretRefUnresolved) {
		t.Errorf("err = %v, want wrapping ErrSecretRefUnresolved", err)
	}
	if !strings.Contains(err.Error(), "op://V/I/F") {
		t.Errorf("err %q does not mention ref", err)
	}
}

// --- Render: integration -------------------------------------------------

func TestRenderRealisticSettingsJSON(t *testing.T) {
	r := mapResolver{
		keychain: map[string]string{"ANTHROPIC_API_KEY": "k-secret"},
		env:      map[string]string{"HOME": "/home/test"},
		op:       map[string]string{"op://Shared/GitHub/token": "ghp_xxx"},
	}
	in := []byte(`{
  "apiKey": "{{ keychain:ANTHROPIC_API_KEY }}",
  "home":   "{{ env.HOME }}",
  "github": "{{ op://Shared/GitHub/token }}",
  "note":   "escaped literal: {{!}}{{ keychain:DONT_RESOLVE }}"
}
`)
	out, err := Render(context.Background(), in, r)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := string(out)
	want := `{
  "apiKey": "k-secret",
  "home":   "/home/test",
  "github": "ghp_xxx",
  "note":   "escaped literal: {{ keychain:DONT_RESOLVE }}"
}
`
	if got != want {
		t.Errorf("Render mismatch:\n got: %q\nwant: %q", got, want)
	}
}
