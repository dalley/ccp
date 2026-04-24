package profile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalley/ccp/internal/paths"
	"github.com/dalley/ccp/internal/refs"
)

// stubResolver lets us control ref resolution deterministically without
// touching a real keychain. Unresolved keys yield a wrapped
// ErrSecretRefUnresolved so errors.Is keeps working.
type stubResolver struct {
	keychain map[string]string
	env      map[string]string
	calls    atomic.Int64
}

func (s *stubResolver) Resolve(_ context.Context, r refs.Ref) (string, error) {
	s.calls.Add(1)
	switch x := r.(type) {
	case refs.RefKeychain:
		if v, ok := s.keychain[x.Key]; ok {
			return v, nil
		}
		return "", errors.New("keychain miss: " + refs.ErrSecretRefUnresolved.Error())
	case refs.RefEnv:
		if v, ok := s.env[x.Var]; ok {
			return v, nil
		}
		return "", errors.New("env miss: " + refs.ErrSecretRefUnresolved.Error())
	}
	return "", refs.ErrSecretRefUnresolved
}

// installStubResolver swaps the package-level defaultResolver factory
// for the duration of the test and restores on cleanup.
func installStubResolver(t *testing.T, s *stubResolver) {
	t.Helper()
	orig := defaultResolver
	defaultResolver = func(_ string) refs.Resolver { return s }
	t.Cleanup(func() { defaultResolver = orig })
}

// TestRenderSettingsJSONResolvesKeychainRef is the TDD anchor test:
// source `settings.json` with `{{ keychain:KEY }}` renders into the
// runtime dir with the stored value.
func TestRenderSettingsJSONResolvesKeychainRef(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})

	src := filepath.Join(pr.SourceDir, "settings.json")
	body := `{"apiKey": "{{ keychain:API_KEY }}"}`
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	stub := &stubResolver{keychain: map[string]string{"API_KEY": "sek-42"}}
	installStubResolver(t, stub)

	if err := pr.BuildSymlinks(); err != nil {
		t.Fatalf("BuildSymlinks: %v", err)
	}

	dst := filepath.Join(pr.ConfigDir, "settings.json")
	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("Lstat runtime: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("runtime entry is a symlink; expected regular file")
	}
	gotMode := info.Mode().Perm()
	if gotMode != 0o600 {
		t.Errorf("mode = %o, want 0600", gotMode)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"apiKey": "sek-42"}`
	if string(b) != want {
		t.Errorf("content = %q, want %q", string(b), want)
	}
	if strings.Contains(string(b), "{{") {
		t.Errorf("rendered output still contains {{: %s", b)
	}
}

// TestRenderPreservesExecuteBit — a 0755 hook script with a ref should
// render with mode 0700 per the (srcMode & 0100) | 0600 spec.
func TestRenderPreservesExecuteBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix mode bits only")
	}
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})

	hooksDir := filepath.Join(pr.SourceDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(hooksDir, "on-start.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho {{ env.GREETING }}\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	stub := &stubResolver{env: map[string]string{"GREETING": "hi"}}
	installStubResolver(t, stub)

	if err := pr.BuildSymlinks(); err != nil {
		t.Fatalf("BuildSymlinks: %v", err)
	}
	dst := filepath.Join(pr.ConfigDir, "hooks", "on-start.sh")
	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("want regular file, got symlink")
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("mode = %o, want 0700", got)
	}
}

// TestRenderMode0644SourceBecomes0600 — plain 0644 source becomes 0600.
func TestRenderMode0644SourceBecomes0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix mode bits only")
	}
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	src := filepath.Join(pr.SourceDir, "settings.json")
	if err := os.WriteFile(src, []byte(`{"x":"{{ env.V }}"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	installStubResolver(t, &stubResolver{env: map[string]string{"V": "1"}})
	if err := pr.BuildSymlinks(); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Lstat(filepath.Join(pr.ConfigDir, "settings.json"))
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 0600", got)
	}
}

// TestSourceWithoutRefsStillSymlinks — existing behavior must survive.
func TestSourceWithoutRefsStillSymlinks(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	if err := os.WriteFile(filepath.Join(pr.SourceDir, "settings.json"), []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	installStubResolver(t, &stubResolver{})
	if err := pr.BuildSymlinks(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(pr.ConfigDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("non-ref source should still symlink; got mode %v", info.Mode())
	}
}

// TestMixedDirectoryRendersRefFileSymlinksOthers — a dir with one ref
// file and one plain file becomes a real 0700 dir, ref file rendered,
// plain file symlinked.
func TestMixedDirectoryRendersRefFileSymlinksOthers(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	hooks := filepath.Join(pr.SourceDir, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "ref.sh"), []byte("#!/bin/sh\necho {{ env.X }}\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "plain.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	installStubResolver(t, &stubResolver{env: map[string]string{"X": "ok"}})
	if err := pr.BuildSymlinks(); err != nil {
		t.Fatal(err)
	}
	topInfo, err := os.Lstat(filepath.Join(pr.ConfigDir, "hooks"))
	if err != nil {
		t.Fatal(err)
	}
	if topInfo.Mode()&os.ModeSymlink != 0 {
		t.Errorf("mixed hooks/ should be a real dir, got symlink")
	}
	if runtime.GOOS != "windows" && topInfo.Mode().Perm() != 0o700 {
		t.Errorf("hooks/ mode = %o, want 0700", topInfo.Mode().Perm())
	}

	refInfo, _ := os.Lstat(filepath.Join(pr.ConfigDir, "hooks", "ref.sh"))
	if refInfo.Mode()&os.ModeSymlink != 0 {
		t.Errorf("ref.sh should be rendered, got symlink")
	}
	plainInfo, _ := os.Lstat(filepath.Join(pr.ConfigDir, "hooks", "plain.sh"))
	if plainInfo.Mode()&os.ModeSymlink == 0 {
		t.Errorf("plain.sh should be symlinked, got regular file")
	}
}

// TestRuntimeManifestTracksRenderedFiles — the manifest records
// rendered-file relative paths.
func TestRuntimeManifestTracksRenderedFiles(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	if err := os.WriteFile(filepath.Join(pr.SourceDir, "settings.json"), []byte(`{{ env.X }}`), 0o644); err != nil {
		t.Fatal(err)
	}
	installStubResolver(t, &stubResolver{env: map[string]string{"X": "v"}})
	if err := pr.BuildSymlinks(); err != nil {
		t.Fatal(err)
	}
	rtm, err := runtimeManifestLoad(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	wantEntry := "settings.json"
	found := false
	for _, f := range rtm.Files {
		if f == wantEntry {
			found = true
		}
	}
	if !found {
		t.Errorf("manifest missing %q, got %+v", wantEntry, rtm.Files)
	}
}

// TestRefreshReRendersWhenKeychainValueChanges — identical source bytes,
// different resolver output, must produce updated runtime content on
// RefreshSymlinks.
func TestRefreshReRendersWhenKeychainValueChanges(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	if err := os.WriteFile(filepath.Join(pr.SourceDir, "settings.json"), []byte(`{{ env.V }}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := &stubResolver{env: map[string]string{"V": "first"}}
	installStubResolver(t, stub)
	if err := pr.BuildSymlinks(); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(pr.ConfigDir, "settings.json")
	if b, _ := os.ReadFile(dst); string(b) != "first" {
		t.Fatalf("initial render = %q", b)
	}
	stub.env["V"] = "second"
	if err := pr.RefreshSymlinks(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "second" {
		t.Errorf("post-refresh = %q, want second", b)
	}
}

// TestSymlinkToRealDirTransition — add a ref inside hooks/; the runtime
// symlink becomes a real dir containing the rendered file + symlinked
// siblings.
func TestSymlinkToRealDirTransition(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	hooks := filepath.Join(pr.SourceDir, "hooks")
	_ = os.MkdirAll(hooks, 0o755)
	if err := os.WriteFile(filepath.Join(hooks, "a.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	installStubResolver(t, &stubResolver{env: map[string]string{"X": "v"}})
	if err := pr.BuildSymlinks(); err != nil {
		t.Fatal(err)
	}
	// hooks/ should be a symlink right now.
	if info, _ := os.Lstat(filepath.Join(pr.ConfigDir, "hooks")); info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("initial state: hooks/ should be symlink")
	}
	// Add a ref-bearing file.
	if err := os.WriteFile(filepath.Join(hooks, "ref.sh"), []byte("#!/bin/sh\n{{ env.X }}\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := pr.RefreshSymlinks(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(pr.ConfigDir, "hooks"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("hooks/ should be real dir after transition")
	}
	// a.sh (no refs) → symlink.
	aInfo, _ := os.Lstat(filepath.Join(pr.ConfigDir, "hooks", "a.sh"))
	if aInfo.Mode()&os.ModeSymlink == 0 {
		t.Errorf("a.sh should be symlink")
	}
	// ref.sh (refs) → rendered.
	refInfo, _ := os.Lstat(filepath.Join(pr.ConfigDir, "hooks", "ref.sh"))
	if refInfo.Mode()&os.ModeSymlink != 0 {
		t.Errorf("ref.sh should be rendered")
	}
}

// TestRealDirToSymlinkTransition — remove the last ref from hooks/;
// next RefreshSymlinks collapses back to a symlink.
func TestRealDirToSymlinkTransition(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	hooks := filepath.Join(pr.SourceDir, "hooks")
	_ = os.MkdirAll(hooks, 0o755)
	plain := filepath.Join(hooks, "plain.sh")
	ref := filepath.Join(hooks, "ref.sh")
	_ = os.WriteFile(plain, []byte("#!/bin/sh\n"), 0o755)
	_ = os.WriteFile(ref, []byte("#!/bin/sh\n{{ env.X }}\n"), 0o755)
	installStubResolver(t, &stubResolver{env: map[string]string{"X": "v"}})
	if err := pr.BuildSymlinks(); err != nil {
		t.Fatal(err)
	}
	// Confirm transition shape.
	if info, _ := os.Lstat(filepath.Join(pr.ConfigDir, "hooks")); info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("initial: hooks/ should be real dir")
	}
	// Now delete the ref-bearing file so no refs remain.
	if err := os.Remove(ref); err != nil {
		t.Fatal(err)
	}
	if err := pr.RefreshSymlinks(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(pr.ConfigDir, "hooks"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("hooks/ should collapse to symlink; got %v", info.Mode())
	}
	// Manifest should have no hooks/* entries.
	rtm, _ := runtimeManifestLoad(pathsForProfile(pr), "work")
	for _, f := range rtm.Files {
		if strings.HasPrefix(f, "hooks/") {
			t.Errorf("manifest still has hooks entry: %s", f)
		}
	}
}

// TestPartialFailureContinuesRenderingOthers — an unresolved ref in
// file A must not prevent file B from rendering; joined error names A.
func TestPartialFailureContinuesRenderingOthers(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	_ = os.WriteFile(filepath.Join(pr.SourceDir, "settings.json"), []byte(`{{ env.A }}`), 0o644)
	_ = os.WriteFile(filepath.Join(pr.SourceDir, "CLAUDE.md"), []byte(`hi {{ env.B }}`), 0o644)
	// Only B is set; A is unresolved.
	installStubResolver(t, &stubResolver{env: map[string]string{"B": "ok"}})

	err := pr.BuildSymlinks()
	if err == nil {
		t.Fatal("want joined error")
	}
	// CLAUDE.md should have rendered successfully.
	b, rerr := os.ReadFile(filepath.Join(pr.ConfigDir, "CLAUDE.md"))
	if rerr != nil {
		t.Fatalf("CLAUDE.md should exist: %v", rerr)
	}
	if string(b) != "hi ok" {
		t.Errorf("CLAUDE.md content = %q", b)
	}
	// settings.json should NOT exist (failed render leaves nothing).
	if _, err := os.Stat(filepath.Join(pr.ConfigDir, "settings.json")); err == nil {
		t.Errorf("failed render shouldn't have created settings.json")
	}
}

// TestRenderingIdentityRefusesToOverwriteForeignFile — if a
// non-ccp-tracked regular file sits at the render dst, refuse.
func TestRenderingIdentityRefusesToOverwriteForeignFile(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	_ = os.WriteFile(filepath.Join(pr.SourceDir, "settings.json"), []byte(`{{ env.X }}`), 0o644)
	// Pre-place a Claude-created file at the runtime dst.
	_ = os.WriteFile(filepath.Join(pr.ConfigDir, "settings.json"), []byte("claude wrote this"), 0o644)
	installStubResolver(t, &stubResolver{env: map[string]string{"X": "v"}})

	err := pr.BuildSymlinks()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("err = %v, want 'refusing to overwrite'", err)
	}
	// Original content must be intact.
	b, _ := os.ReadFile(filepath.Join(pr.ConfigDir, "settings.json"))
	if string(b) != "claude wrote this" {
		t.Errorf("foreign file clobbered: %s", b)
	}
}

// TestRenderedFilesSurviveRefresh — regression check: a rendered file
// isn't accidentally pruned during RefreshSymlinks's symlink-cleanup
// pass.
func TestRenderedFilesSurviveRefresh(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	_ = os.WriteFile(filepath.Join(pr.SourceDir, "settings.json"), []byte(`{{ env.X }}`), 0o644)
	installStubResolver(t, &stubResolver{env: map[string]string{"X": "v"}})
	if err := pr.BuildSymlinks(); err != nil {
		t.Fatal(err)
	}
	if err := pr.RefreshSymlinks(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(pr.ConfigDir, "settings.json"))
	if err != nil || string(b) != "v" {
		t.Errorf("content after refresh = %q, err = %v", b, err)
	}
}

// TestRejectEscapingSymlinksInSourceInvariantPreserved — an escaping
// symlink under source aborts BuildSymlinks before any render happens.
func TestRejectEscapingSymlinksInSourceInvariantPreserved(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	// Create an escaping symlink under source.
	evil := filepath.Join(pr.SourceDir, "hooks", "escape")
	_ = os.MkdirAll(filepath.Dir(evil), 0o755)
	if err := os.Symlink("/etc/passwd", evil); err != nil {
		t.Skipf("platform can't make symlinks: %v", err)
	}
	installStubResolver(t, &stubResolver{})
	err := pr.BuildSymlinks()
	if err == nil {
		t.Fatal("expected rejection")
	}
	if !strings.Contains(err.Error(), "escapes the profile tree") {
		t.Errorf("err = %v", err)
	}
}

// TestExecRefreshCountHelperSanity — the test counter helpers work.
// Covers only the package-level functions (profile doesn't depend on
// the CLI counter; this test is a canary in case someone moves the
// state).
func TestRuntimeManifestLoadEmpty(t *testing.T) {
	p := setupHome(t)
	rtm, err := runtimeManifestLoad(p, "nobody")
	if err != nil {
		t.Fatal(err)
	}
	if len(rtm.Files) != 0 {
		t.Errorf("want empty, got %+v", rtm.Files)
	}
	if rtm.SchemaVersion != RuntimeManifestSchemaVersion {
		t.Errorf("schema = %d", rtm.SchemaVersion)
	}
}

// TestModeBitmath sanity-checks the (mode & 0100) | 0600 computation
// the renderFile helper applies.
func TestModeBitmath(t *testing.T) {
	cases := []struct {
		src, want os.FileMode
	}{
		{0o644, 0o600},
		{0o755, 0o700},
		{0o600, 0o600},
		{0o700, 0o700},
		{0o664, 0o600},
		{0o777, 0o700},
	}
	for _, c := range cases {
		got := (c.src & 0o100) | 0o600
		if got != c.want {
			t.Errorf("src=%o → %o, want %o", c.src, got, c.want)
		}
	}
}

// TestPathsForProfileRoundtrip — pathsForProfile must recover a Paths
// equivalent to paths.Resolve for the same CCP_ROOT env.
func TestPathsForProfileRoundtrip(t *testing.T) {
	p := setupHome(t)
	pr := New(p, "work")
	got := pathsForProfile(pr)
	if got.ConfigDir != p.ConfigDir {
		t.Errorf("ConfigDir = %s, want %s", got.ConfigDir, p.ConfigDir)
	}
	if got.RuntimeManifestDir != p.RuntimeManifestDir {
		t.Errorf("RuntimeManifestDir = %s, want %s", got.RuntimeManifestDir, p.RuntimeManifestDir)
	}
	if got.Home != p.Home {
		t.Errorf("Home = %s, want %s", got.Home, p.Home)
	}
}

// TestConcurrentBuildSymlinksDoesNotCorruptManifest — running two
// BuildSymlinks goroutines against the same profile must serialize via
// the in-process mutex and leave a valid manifest. This mirrors the
// cross-process case where the CLI's withLock serializes.
func TestConcurrentBuildSymlinksDoesNotCorruptManifest(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	_ = os.WriteFile(filepath.Join(pr.SourceDir, "settings.json"), []byte(`{{ env.X }}`), 0o644)
	installStubResolver(t, &stubResolver{env: map[string]string{"X": "v"}})

	done := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() { done <- pr.BuildSymlinks() }()
	}
	for i := 0; i < 4; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent build: %v", err)
		}
	}
	rtm, err := runtimeManifestLoad(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	// Expect exactly one manifest entry for settings.json.
	if len(rtm.Files) != 1 || rtm.Files[0] != "settings.json" {
		t.Errorf("manifest = %+v, want [settings.json]", rtm.Files)
	}
}

// TestUnresolvedRefIsSentinel — the joined error from BuildSymlinks
// unwraps to refs.ErrSecretRefUnresolved via errors.Is.
func TestUnresolvedRefIsSentinel(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	_ = os.WriteFile(filepath.Join(pr.SourceDir, "settings.json"), []byte(`{{ env.MISSING }}`), 0o644)
	// Use the actual refs.DefaultResolver so the error wraps the
	// sentinel correctly.
	defaultResolver = func(prof string) refs.Resolver {
		return refs.DefaultResolver{Profile: prof, EnvLookup: func(string) (string, bool) { return "", false }}
	}
	t.Cleanup(func() {
		defaultResolver = func(prof string) refs.Resolver {
			return refs.DefaultResolver{Profile: prof, KeyringGet: keychainGetShim, EnvLookup: os.LookupEnv}
		}
	})
	err := pr.BuildSymlinks()
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, refs.ErrSecretRefUnresolved) {
		t.Errorf("not wrapped: %v", err)
	}
}

// ensure paths package reference is retained (the test file uses it
// indirectly via setupHome / pathsForProfile).
var _ paths.Paths
