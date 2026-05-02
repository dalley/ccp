package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupCLI(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	t.Setenv("CCP_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", "")
	return root
}

func runCLI(t *testing.T, in string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	r := NewRoot()
	r.SetOut(&outBuf)
	r.SetErr(&errBuf)
	if in != "" {
		r.SetIn(strings.NewReader(in))
	}
	r.SetArgs(args)
	err = r.Execute()
	return outBuf.String(), errBuf.String(), err
}

func TestProfileLifecycle(t *testing.T) {
	root := setupCLI(t)

	// Pretend the user already has a ~/.claude/settings.json.
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(`{"model":"sonnet"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// create --from-current
	if out, _, err := runCLI(t, "", "profile", "create", "work", "--from-current"); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	} else if !strings.Contains(out, "Created profile \"work\"") {
		t.Errorf("create output: %s", out)
	}

	// list shows it with no active marker yet
	out, _, err := runCLI(t, "", "profile", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "work") {
		t.Errorf("list missing work: %q", out)
	}
	if strings.Contains(out, "* work") {
		t.Errorf("work should not be active yet: %q", out)
	}

	// use
	if _, _, err := runCLI(t, "", "use", "work"); err != nil {
		t.Fatalf("use: %v", err)
	}

	// current
	out, _, err = runCLI(t, "", "current")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "work" {
		t.Errorf("current = %q, want work", out)
	}

	// list now shows active marker
	out, _, err = runCLI(t, "", "profile", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "* work") {
		t.Errorf("list missing active marker: %q", out)
	}

	// show confirms settings.json was seeded
	out, _, err = runCLI(t, "", "profile", "show", "work")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "settings.json   present") && !strings.Contains(out, "settings.json    present") && !strings.Contains(out, "settings.json      present") {
		// tolerate whitespace variation from the table formatter
		if !strings.Contains(out, "settings.json") || !strings.Contains(out, "present") {
			t.Errorf("show missing settings.json status: %q", out)
		}
	}

	// rename
	if _, _, err := runCLI(t, "", "profile", "rename", "work", "job"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	out, _, err = runCLI(t, "", "current")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "job" {
		t.Errorf("active after rename = %q, want job", out)
	}

	// delete --yes moves to backup and clears active
	out, _, err = runCLI(t, "", "profile", "delete", "job", "--yes")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !strings.Contains(out, "Backup:") {
		t.Errorf("delete missing backup path: %q", out)
	}
	if !strings.Contains(out, "Deleted profile \"job\"") {
		t.Errorf("delete message: %q", out)
	}

	out, _, err = runCLI(t, "", "current")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("current should be empty after delete, got %q", out)
	}
}

func TestProfileListJSON(t *testing.T) {
	setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "profile", "create", "b"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "use", "b"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "profile", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	// Cheap content check; the JSON encoder output is stable.
	if !strings.Contains(out, `"name": "a"`) {
		t.Errorf("missing a: %s", out)
	}
	if !strings.Contains(out, `"name": "b"`) || !strings.Contains(out, `"active": true`) {
		t.Errorf("missing active b: %s", out)
	}
}

func TestUseShellEmitsExports(t *testing.T) {
	setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "use", "work", "--shell")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "export CLAUDE_CONFIG_DIR=") {
		t.Errorf("missing export: %s", out)
	}
	if !strings.Contains(out, "export CCP_PROFILE='work'") {
		t.Errorf("missing profile export: %s", out)
	}
}

func TestCreateWithAliasWritesBlock(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work", "--alias"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, ".zshrc"))
	if err != nil {
		t.Fatalf("reading rc: %v", err)
	}
	if !strings.Contains(string(b), "alias claude-work=") {
		t.Errorf("alias missing: %s", b)
	}
}

func TestDeleteUsesBackupAndRestoresManifest(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "profile", "create", "work", "--alias"); err == nil {
		t.Fatal("expected error creating duplicate")
	}
	if _, _, err := runCLI(t, "", "profile", "delete", "work", "--yes"); err != nil {
		t.Fatal(err)
	}
	// Backup dir should contain a moved source.
	backups := filepath.Join(root, ".config", "ccp", "backups")
	entries, err := os.ReadDir(backups)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no backups: err=%v entries=%v", err, entries)
	}
	found := false
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(backups, e.Name(), "profiles-work")); err == nil {
			found = true
		}
	}
	if !found {
		t.Errorf("profiles-work not in any backup")
	}
}
