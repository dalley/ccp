package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffBetweenTwoProfiles(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "profile", "create", "b"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/a/settings.json"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/b/settings.json"), []byte("B"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Diff with actual differences returns ErrDiffFound (exit code 4) so
	// agents can branch without parsing stdout. JSON/prose still lands.
	out, _, err := runCLI(t, "", "profile", "diff", "a", "b")
	if err == nil {
		t.Fatal("expected ErrDiffFound when profiles differ")
	}
	if ExitCodeFor(err) != ExitConflict {
		t.Errorf("exit code = %d, want %d (conflict)", ExitCodeFor(err), ExitConflict)
	}
	if !strings.Contains(out, "~ changed") || !strings.Contains(out, "settings.json") {
		t.Errorf("diff output missing changed marker:\n%s", out)
	}
}

func TestDiffIdenticalProfilesReportsIdentical(t *testing.T) {
	setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "profile", "create", "b", "--from", "a"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "profile", "diff", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "identical") {
		t.Errorf("expected 'identical' in output:\n%s", out)
	}
}

func TestDoctorHealthyReportsSuccess(t *testing.T) {
	setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "profile", "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if !strings.Contains(out, "healthy") {
		t.Errorf("expected 'healthy' marker:\n%s", out)
	}
}

func TestDoctorDetectsDanglingSymlinkAndExitsNonzero(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, ".config/ccp/profiles/work/settings.json")
	if err := os.WriteFile(src, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Trigger symlink creation via `use` (BuildSymlinks happens at create
	// time, but we wrote the file afterward — re-use refresh via a
	// throwaway symlink link step using the source).
	// Simplest: just manually link since we know the layout.
	runtime := filepath.Join(root, ".claude-work/settings.json")
	if err := os.Symlink(src, runtime); err != nil {
		t.Fatal(err)
	}
	// Now remove the source — the runtime link becomes dangling.
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "profile", "doctor", "work")
	if err == nil {
		t.Errorf("expected nonzero exit on dangling symlink")
	}
	if !strings.Contains(out, "dangling") {
		t.Errorf("expected 'dangling' in output:\n%s", out)
	}
}

func TestRollbackRestoresLastDelete(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, ".config/ccp/profiles/work/settings.json")
	if err := os.WriteFile(marker, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "profile", "delete", "work", "--yes"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("source wasn't removed: %v", err)
	}
	out, _, err := runCLI(t, "", "profile", "rollback")
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !strings.Contains(out, "work") {
		t.Errorf("rollback output missing work:\n%s", out)
	}
	b, err := os.ReadFile(marker)
	if err != nil || string(b) != "orig" {
		t.Errorf("restore content = %q, err = %v", b, err)
	}
}

func TestExecSetsClaudeConfigDirForChildProcess(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	// /bin/sh -c 'echo $CLAUDE_CONFIG_DIR' gives us an observable effect.
	out, _, err := runCLI(t, "", "exec", "work", "--", "/bin/sh", "-c", "echo $CLAUDE_CONFIG_DIR")
	if err != nil {
		t.Fatalf("exec: %v\n%s", err, out)
	}
	want := filepath.Join(root, ".claude-work")
	if !strings.Contains(out, want) {
		t.Errorf("child didn't see CLAUDE_CONFIG_DIR=%q\nout: %s", want, out)
	}
}

// TestExecResolvesKeychainRefsEndToEnd is the integration test that
// closes the loop: a profile with a keychain ref in settings.json; exec
// triggers a refresh; child process cats the rendered content and sees
// the resolved value, not the `{{ ... }}` token.
func TestExecResolvesKeychainRefsEndToEnd(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	// Write an env-bearing settings.json so we don't depend on a real
	// keychain during test — env is the same `{{ ... }}` render path.
	src := filepath.Join(root, ".config/ccp/profiles/work/settings.json")
	if err := os.WriteFile(src, []byte(`{"k":"{{ env.CCP_EXEC_TEST_SECRET }}"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCP_EXEC_TEST_SECRET", "shhh")

	runtimeFile := filepath.Join(root, ".claude-work/settings.json")
	out, _, err := runCLI(t, "", "exec", "work", "--", "/bin/cat", runtimeFile)
	if err != nil {
		t.Fatalf("exec: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"k":"shhh"`) {
		t.Errorf("expected rendered value in %q", out)
	}
	if strings.Contains(out, "{{") {
		t.Errorf("runtime still contains {{: %s", out)
	}
}

// TestExecSkipsRefreshForProfilesWithoutRefs — no refs under source
// means RefreshSymlinks must NOT be invoked (legacy fast path).
func TestExecSkipsRefreshForProfilesWithoutRefs(t *testing.T) {
	setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	ResetExecRefreshCount()
	if _, _, err := runCLI(t, "", "exec", "work", "--", "/bin/sh", "-c", ":"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if got := LoadExecRefreshCount(); got != 0 {
		t.Errorf("refresh count = %d, want 0 (no refs means skip)", got)
	}
}

// TestExecNoRefreshFlagSkipsRefreshEvenWithRefs — power-user escape
// hatch: --no-refresh short-circuits even when the profile has refs.
func TestExecNoRefreshFlagSkipsRefreshEvenWithRefs(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	// Ref-bearing source.
	src := filepath.Join(root, ".config/ccp/profiles/work/settings.json")
	if err := os.WriteFile(src, []byte(`{"k":"{{ env.Y }}"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("Y", "v")
	ResetExecRefreshCount()
	if _, _, err := runCLI(t, "", "exec", "--no-refresh", "work", "--", "/bin/sh", "-c", ":"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if got := LoadExecRefreshCount(); got != 0 {
		t.Errorf("refresh count = %d, want 0 with --no-refresh", got)
	}
}

// TestExecTriggersRefreshForRefBearingProfile — confirms the opposite
// of the two above: refs present and no flag → refresh runs exactly
// once.
func TestExecTriggersRefreshForRefBearingProfile(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, ".config/ccp/profiles/work/settings.json")
	if err := os.WriteFile(src, []byte(`{"k":"{{ env.Z }}"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("Z", "v")
	ResetExecRefreshCount()
	if _, _, err := runCLI(t, "", "exec", "work", "--", "/bin/sh", "-c", ":"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if got := LoadExecRefreshCount(); got != 1 {
		t.Errorf("refresh count = %d, want 1", got)
	}
}
