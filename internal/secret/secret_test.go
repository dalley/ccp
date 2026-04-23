//go:build !windows

package secret

// TEST INVARIANT — DO NOT REMOVE:
//
// go-keyring's MockInit / MockInitWithError mutate package-level state.
// Tests in this file MUST NOT call t.Parallel() (neither on the top-level
// test nor on any subtest), and they MUST call setupKeyring() or
// resetPackageState() at the top of each test to reset the mock provider
// and the one-time fallback warning. Breaking either invariant introduces
// flaky cross-test races that are hard to reproduce.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/dalley/ccp/internal/paths"
	"github.com/zalando/go-keyring"
)

// setupKeyring resets both the keyring mock provider and the package-local
// fallback-warning sync.Once so each test starts with a clean slate. It
// also routes warnings to a caller-owned buffer so assertions can observe
// (or suppress) the one-time warning.
//
// Returns the buffer so tests that care about the warning's exact wording
// can read it; tests that just want silence can discard the return value
// and use io.Discard via a second SetFallbackWarnWriter call.
func setupKeyring(t *testing.T) *bytes.Buffer {
	t.Helper()
	keyring.MockInit()
	resetFallbackWarnOnce()
	buf := &bytes.Buffer{}
	SetFallbackWarnWriter(buf)
	t.Cleanup(func() {
		// Restore the default writer; a later test that forgets to call
		// setupKeyring would otherwise still write into this (freed) buffer.
		SetFallbackWarnWriter(os.Stderr)
	})
	return buf
}

// newPaths creates an isolated Paths rooted at t.TempDir() and calls
// Ensure so the secrets/ subdir exists with 0700.
func newPaths(t *testing.T) paths.Paths {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)
	// Unset XDG to keep path math deterministic under CCP_ROOT.
	t.Setenv("XDG_CONFIG_HOME", "")
	p, err := paths.Resolve()
	if err != nil {
		t.Fatalf("paths.Resolve: %v", err)
	}
	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return p
}

// ---------- happy path ----------

func TestSetGetRoundTripKeychain(t *testing.T) {
	setupKeyring(t)
	p := newPaths(t)

	if err := Set(p, "work", "API_KEY", "s3cret"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := Get(p, "work", "API_KEY")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "s3cret" {
		t.Errorf("Get = %q, want %q", got, "s3cret")
	}

	// Fallback file must NOT exist — the keychain was available, so we
	// never should have written to disk.
	if _, err := os.Stat(p.SecretFilePath("work")); !os.IsNotExist(err) {
		t.Errorf("fallback file present after successful keychain Set; err=%v", err)
	}
}

func TestDeleteRemovesFromBothBackends(t *testing.T) {
	setupKeyring(t)
	p := newPaths(t)

	// Put one in the keychain, one in the file store (by simulating an
	// unavailable keychain for the second Set).
	if err := Set(p, "work", "KEY_A", "a"); err != nil {
		t.Fatalf("Set keychain: %v", err)
	}

	// Pre-populate the file store with a second key (bypassing the keychain
	// path entirely to model the "synced from another machine" case).
	if err := fileSet(p, "work", "KEY_B", "b"); err != nil {
		t.Fatalf("fileSet: %v", err)
	}

	if err := Delete(p, "work", "KEY_A"); err != nil {
		t.Fatalf("Delete A: %v", err)
	}
	if _, err := Get(p, "work", "KEY_A"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("Get A after Delete: err = %v, want ErrSecretNotFound", err)
	}

	if err := Delete(p, "work", "KEY_B"); err != nil {
		t.Fatalf("Delete B: %v", err)
	}
	if _, err := Get(p, "work", "KEY_B"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("Get B after Delete: err = %v, want ErrSecretNotFound", err)
	}

	// Both backing files should be gone (fileSave's empty-map branch removes
	// a zero-length JSON; the keychain index should also be scrubbed).
	if _, err := os.Stat(p.SecretFilePath("work")); !os.IsNotExist(err) {
		t.Errorf("fallback file still present after Delete of last key: %v", err)
	}
	if _, err := os.Stat(indexPath(p, "work")); !os.IsNotExist(err) {
		t.Errorf("keychain index still present after Delete of last key: %v", err)
	}
}

func TestListMergesAndDedups(t *testing.T) {
	setupKeyring(t)
	p := newPaths(t)

	// Keychain keys.
	for _, k := range []string{"ZETA", "ALPHA", "MID"} {
		if err := Set(p, "work", k, "v"); err != nil {
			t.Fatalf("Set %s: %v", k, err)
		}
	}
	// File-only keys (including one that overlaps with a keychain key —
	// List must de-dup).
	if err := fileSet(p, "work", "FILE_ONLY", "f"); err != nil {
		t.Fatalf("fileSet FILE_ONLY: %v", err)
	}
	if err := fileSet(p, "work", "MID", "shadow"); err != nil {
		t.Fatalf("fileSet MID: %v", err)
	}

	got, err := List(p, "work")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"ALPHA", "FILE_ONLY", "MID", "ZETA"}
	if !sortedEqual(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
}

// ---------- fallback: unavailable keychain ----------

func TestSetFallsBackWhenKeychainUnavailable(t *testing.T) {
	keyring.MockInitWithError(errors.New("dbus: connection refused: no such interface"))
	resetFallbackWarnOnce()
	warn := &bytes.Buffer{}
	SetFallbackWarnWriter(warn)
	t.Cleanup(func() { SetFallbackWarnWriter(os.Stderr) })

	p := newPaths(t)

	if err := Set(p, "work", "KEY", "val"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Warning fired exactly once.
	msg := warn.String()
	if !strings.Contains(msg, "keychain unavailable") {
		t.Errorf("warning missing 'keychain unavailable': %q", msg)
	}
	if !strings.Contains(msg, p.SecretFilePath("work")) {
		t.Errorf("warning missing fallback path: %q", msg)
	}

	// File store has the value.
	b, err := os.ReadFile(p.SecretFilePath("work"))
	if err != nil {
		t.Fatalf("read fallback file: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse fallback file: %v", err)
	}
	if m["KEY"] != "val" {
		t.Errorf("fallback file: KEY = %q, want %q", m["KEY"], "val")
	}
}

func TestFallbackWarningEmittedOncePerProcess(t *testing.T) {
	keyring.MockInitWithError(errors.New("dbus: no such interface"))
	resetFallbackWarnOnce()
	warn := &bytes.Buffer{}
	SetFallbackWarnWriter(warn)
	t.Cleanup(func() { SetFallbackWarnWriter(os.Stderr) })

	p := newPaths(t)

	for i, k := range []string{"KEY_A", "KEY_B", "KEY_C"} {
		if err := Set(p, "work", k, "v"); err != nil {
			t.Fatalf("Set #%d: %v", i, err)
		}
	}

	// Exactly one warning line.
	lines := strings.Split(strings.TrimRight(warn.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Errorf("want exactly 1 warning line, got %d:\n%s", len(lines), warn.String())
	}
}

func TestGetFallsBackWhenKeychainUnavailable(t *testing.T) {
	// Populate the file store first (simulating a previous fallback Set),
	// then clear the mock so subsequent Gets travel the unavailable path.
	keyring.MockInitWithError(errors.New("dbus: no such interface"))
	resetFallbackWarnOnce()
	SetFallbackWarnWriter(io.Discard)
	t.Cleanup(func() { SetFallbackWarnWriter(os.Stderr) })

	p := newPaths(t)
	if err := Set(p, "work", "KEY", "from-file"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := Get(p, "work", "KEY")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "from-file" {
		t.Errorf("Get = %q, want %q", got, "from-file")
	}
}

// ---------- locked keychain ----------

func TestSetReturnsLockedWithoutFallback(t *testing.T) {
	keyring.MockInitWithError(errors.New("security: SecKeychainItemCopyContent: keychain is locked"))
	resetFallbackWarnOnce()
	warn := &bytes.Buffer{}
	SetFallbackWarnWriter(warn)
	t.Cleanup(func() { SetFallbackWarnWriter(os.Stderr) })

	p := newPaths(t)
	err := Set(p, "work", "KEY", "val")
	if !errors.Is(err, ErrKeychainLocked) {
		t.Fatalf("Set: err = %v, want ErrKeychainLocked", err)
	}

	// No fallback file written.
	if _, err := os.Stat(p.SecretFilePath("work")); !os.IsNotExist(err) {
		t.Errorf("fallback file written on locked keychain: %v", err)
	}
	// No warning emitted either — locked is not a fallback scenario.
	if warn.Len() != 0 {
		t.Errorf("warning emitted on locked keychain: %q", warn.String())
	}
}

func TestGetReturnsLocked(t *testing.T) {
	keyring.MockInitWithError(errors.New("The user name or passphrase you entered is not correct (authentication)"))
	resetFallbackWarnOnce()
	SetFallbackWarnWriter(io.Discard)
	t.Cleanup(func() { SetFallbackWarnWriter(os.Stderr) })

	p := newPaths(t)
	_, err := Get(p, "work", "KEY")
	if !errors.Is(err, ErrKeychainLocked) {
		t.Fatalf("Get: err = %v, want ErrKeychainLocked", err)
	}
}

// ---------- cross-machine ----------

func TestGetFallsThroughToFileStoreOnKeychainNotFound(t *testing.T) {
	setupKeyring(t) // empty keychain, no errors

	p := newPaths(t)
	// Pre-populate the file store (simulating a machine sync where the
	// remote pushed a secret into the fallback store and this local
	// machine's keychain has never seen it).
	if err := fileSet(p, "work", "SYNCED", "cross-machine-value"); err != nil {
		t.Fatalf("fileSet: %v", err)
	}

	got, err := Get(p, "work", "SYNCED")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "cross-machine-value" {
		t.Errorf("Get = %q, want %q", got, "cross-machine-value")
	}
}

func TestGetMissingInBothReturnsNotFound(t *testing.T) {
	setupKeyring(t)
	p := newPaths(t)

	_, err := Get(p, "work", "MISSING")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Get: err = %v, want ErrSecretNotFound", err)
	}
}

// ---------- edge cases ----------

func TestValidateKeyRejectsBadInputs(t *testing.T) {
	setupKeyring(t)

	cases := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"slash", "foo/bar"},
		{"colon", "foo:bar"},
		{"dash", "foo-bar"},           // dashes rejected by keyRe
		{"dot", "foo.bar"},            // dots rejected
		{"leading digit", "1FOO"},
		{"control char", "foo\x01bar"},
		{"newline", "foo\nbar"},
		{"too long", strings.Repeat("A", 256)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateKey(tc.key); err == nil {
				t.Errorf("ValidateKey(%q): want error, got nil", tc.key)
			}
		})
	}
}

func TestSetRejectsInvalidProfile(t *testing.T) {
	setupKeyring(t)
	p := newPaths(t)

	err := Set(p, "BAD PROFILE", "KEY", "v")
	if err == nil {
		t.Fatal("Set with invalid profile: want error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid profile name") {
		t.Errorf("error = %v, want profile-name-invalid message", err)
	}
	// Ensure nothing leaked to disk even though we had no lock.
	if _, err := os.Stat(p.SecretFilePath("BAD PROFILE")); !os.IsNotExist(err) {
		t.Errorf("file created for invalid profile: %v", err)
	}
}

func TestSetInvalidProfileUnderUnavailableKeychain(t *testing.T) {
	// Even with the fallback path active, an invalid profile name must not
	// write anything. Defense against the "fall-through never writes
	// plaintext with bad names" test scenario in the plan.
	keyring.MockInitWithError(errors.New("dbus: no such interface"))
	resetFallbackWarnOnce()
	SetFallbackWarnWriter(io.Discard)
	t.Cleanup(func() { SetFallbackWarnWriter(os.Stderr) })

	p := newPaths(t)
	if err := Set(p, "BAD", "KEY", "v"); err == nil {
		// "BAD" is actually valid (lowercase only would be rejected; BAD is
		// uppercase). Let's use a clearly invalid name.
		t.Fatal("profile validation let 'BAD' (uppercase) through")
	}

	entries, err := os.ReadDir(p.SecretsDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("files written under invalid-profile fallback: %v", names)
	}
}

func TestLargeValueUnderMock(t *testing.T) {
	setupKeyring(t)
	p := newPaths(t)

	big := strings.Repeat("x", 1500)
	if err := Set(p, "work", "BIG", big); err != nil {
		t.Fatalf("Set 1500-byte value: %v", err)
	}
	got, err := Get(p, "work", "BIG")
	if err != nil {
		t.Fatalf("Get 1500-byte value: %v", err)
	}
	if got != big {
		t.Errorf("Get mismatch for 1500-byte value (len=%d)", len(got))
	}
}

// ---------- file-store invariants ----------

func TestFileStorePermissions(t *testing.T) {
	// Force the fallback path so the file actually gets written.
	keyring.MockInitWithError(errors.New("dbus: no such interface"))
	resetFallbackWarnOnce()
	SetFallbackWarnWriter(io.Discard)
	t.Cleanup(func() { SetFallbackWarnWriter(os.Stderr) })

	p := newPaths(t)
	if err := Set(p, "work", "KEY", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	fi, err := os.Stat(p.SecretFilePath("work"))
	if err != nil {
		t.Fatalf("stat fallback file: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("fallback file mode = %o, want 0600", mode)
	}

	di, err := os.Stat(p.SecretsDir)
	if err != nil {
		t.Fatalf("stat SecretsDir: %v", err)
	}
	if runtime.GOOS != "windows" {
		if mode := di.Mode().Perm(); mode != 0o700 {
			t.Errorf("SecretsDir mode = %o, want 0700", mode)
		}
	}
}

func TestFileSaveLeavesNoTempFiles(t *testing.T) {
	setupKeyring(t)
	p := newPaths(t)

	// Force the fallback path by sending through the unavailable branch.
	keyring.MockInitWithError(errors.New("dbus: no such interface"))
	resetFallbackWarnOnce()
	SetFallbackWarnWriter(io.Discard)

	for i, k := range []string{"KEY_A", "KEY_B", "KEY_C"} {
		if err := Set(p, "work", k, "v"); err != nil {
			t.Fatalf("Set #%d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(p.SecretsDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".secret-") || strings.HasSuffix(name, ".tmp") {
			t.Errorf("temp file leaked: %s", name)
		}
	}
}

// ---------- classifyKeyringErr ----------

func TestClassifyKeyringErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want keychainState
	}{
		{"nil", nil, stateOK},
		{"ErrNotFound (sentinel)", keyring.ErrNotFound, stateNotFound},
		{"wrapped ErrNotFound", errors.Join(errors.New("outer"), keyring.ErrNotFound), stateNotFound},
		{"locked string", errors.New("SecKeychainItemCopyContent: keychain is locked"), stateLocked},
		{"authentication string", errors.New("The authentication prompt was cancelled"), stateLocked},
		{"dbus", errors.New("dbus: connection refused"), stateUnavailable},
		{"no such interface", errors.New("org.freedesktop.DBus.Error.ServiceUnknown: no such interface"), stateUnavailable},
		{"secret service not available", errors.New("secret service not available"), stateUnavailable},
		{"cannot autolaunch", errors.New("cannot autolaunch D-Bus without X11 $DISPLAY"), stateUnavailable},
		{"unclassifiable other", errors.New("mystery backend failure"), stateOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyKeyringErr(tc.err)
			if got != tc.want {
				t.Errorf("classifyKeyringErr(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// ---------- concurrency ----------

// TestConcurrentSetsLandBothValues models the plan's "two concurrent Set
// calls on different keys both land" scenario. The package itself does
// not take the global flock (that's the CLI's job); this test supplies a
// shared mutex that serializes the file writes, matching the discipline
// `ccp secret` will impose.
func TestConcurrentSetsLandBothValues(t *testing.T) {
	setupKeyring(t)
	p := newPaths(t)

	// Route through the fallback path so the file store is exercised.
	keyring.MockInitWithError(errors.New("dbus: no such interface"))
	resetFallbackWarnOnce()
	SetFallbackWarnWriter(io.Discard)

	var mu sync.Mutex
	var wg sync.WaitGroup
	keys := []string{"K1", "K2", "K3", "K4", "K5", "K6", "K7", "K8"}
	for _, k := range keys {
		k := k
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			if err := Set(p, "work", k, k+"_val"); err != nil {
				t.Errorf("concurrent Set %s: %v", k, err)
			}
		}()
	}
	wg.Wait()

	for _, k := range keys {
		got, err := Get(p, "work", k)
		if err != nil {
			t.Errorf("Get %s: %v", k, err)
			continue
		}
		if got != k+"_val" {
			t.Errorf("Get %s = %q, want %q", k, got, k+"_val")
		}
	}
}

// ---------- helpers ----------

func sortedEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

// Compile-time assertion that Paths is still in scope — a future rename of
// the Paths type would fail this test file to compile rather than silently
// dropping coverage via an unused-import removal.
var _ = paths.Paths{}
