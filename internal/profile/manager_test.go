package profile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalley/ccp/internal/paths"
)

func setupHome(t *testing.T) paths.Paths {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", "")
	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestValidateName(t *testing.T) {
	ok := []string{"work", "w1", "my-profile", "my_profile", "a"}
	bad := []string{"", "Work", "1work", "-work", "work!", "a" + string(make([]byte, 100))}
	for _, n := range ok {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	for _, n := range bad {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", n)
		}
	}
}

func TestCreateFromScratch(t *testing.T) {
	p := setupHome(t)
	pr, err := Create(p, "work", CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !pr.Exists() {
		t.Fatalf("source dir missing: %s", pr.SourceDir)
	}
	if _, err := os.Stat(pr.ConfigDir); err != nil {
		t.Errorf("config dir missing: %v", err)
	}
}

func TestCreateRejectsDuplicate(t *testing.T) {
	p := setupHome(t)
	if _, err := Create(p, "work", CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err := Create(p, "work", CreateOptions{})
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("want ErrAlreadyExists, got %v", err)
	}
}

func TestCreateFromCurrentSeedsFromClaudeHome(t *testing.T) {
	p := setupHome(t)
	// Pretend the user has an existing ~/.claude/settings.json + agents/.
	if err := os.MkdirAll(filepath.Join(p.ClaudeHome, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(p.ClaudeHome, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"model":"sonnet"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(p.ClaudeHome, "agents", "greeter.md")
	if err := os.WriteFile(agent, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	pr, err := Create(p, "work", CreateOptions{FromCurrent: true})
	if err != nil {
		t.Fatal(err)
	}
	// Settings copied.
	b, err := os.ReadFile(filepath.Join(pr.SourceDir, "settings.json"))
	if err != nil || string(b) != `{"model":"sonnet"}` {
		t.Errorf("settings.json = %q, err = %v", b, err)
	}
	// Agent copied.
	b, err = os.ReadFile(filepath.Join(pr.SourceDir, "agents", "greeter.md"))
	if err != nil || string(b) != "hi" {
		t.Errorf("agent content = %q, err = %v", b, err)
	}
	// Runtime dir has the symlink pointing at the source.
	link, err := os.Readlink(filepath.Join(pr.ConfigDir, "settings.json"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if link != filepath.Join(pr.SourceDir, "settings.json") {
		t.Errorf("symlink target = %q", link)
	}
}

func TestCreateFromProfileClonesSource(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	if err := os.WriteFile(filepath.Join(pr.SourceDir, "settings.json"), []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Clone it.
	cloned, err := Create(p, "work2", CreateOptions{FromProfile: "work"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(cloned.SourceDir, "settings.json"))
	if err != nil || string(b) != "orig" {
		t.Errorf("cloned settings = %q, err = %v", b, err)
	}
}

func TestListReturnsSortedNames(t *testing.T) {
	p := setupHome(t)
	for _, n := range []string{"zebra", "alpha", "middle"} {
		if _, err := Create(p, n, CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := List(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "middle", "zebra"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, pr := range got {
		if pr.Name != want[i] {
			t.Errorf("[%d] = %q, want %q", i, pr.Name, want[i])
		}
	}
}

func TestDeleteMovesEverythingToBackup(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	if err := os.WriteFile(filepath.Join(pr.SourceDir, "settings.json"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate a runtime auth file (something Claude would create) to
	// confirm Delete captures it into the backup, not just the symlinked
	// shared content.
	if err := os.WriteFile(filepath.Join(pr.ConfigDir, "auth.json"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	bkDir := filepath.Join(p.BackupsDir, "pre-delete-work")
	returned, err := Delete(p, "work", bkDir)
	if err != nil {
		t.Fatal(err)
	}
	if returned != bkDir {
		t.Errorf("Delete returned %q, want %q", returned, bkDir)
	}
	if _, err := os.Stat(pr.SourceDir); !os.IsNotExist(err) {
		t.Errorf("source dir should be gone: %v", err)
	}
	if _, err := os.Stat(pr.ConfigDir); !os.IsNotExist(err) {
		t.Errorf("runtime dir should be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bkDir, "profiles-work", "settings.json")); err != nil {
		t.Errorf("source not moved to backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bkDir, "claude-work", "auth.json")); err != nil {
		t.Errorf("runtime not moved to backup: %v", err)
	}
}

func TestRenameUpdatesSymlinksToPointAtNewSource(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "old", CreateOptions{})
	if err := os.WriteFile(filepath.Join(pr.SourceDir, "settings.json"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Materialize the symlink that references settings.json.
	if err := pr.RefreshSymlinks(); err != nil {
		t.Fatal(err)
	}

	if err := Rename(p, "old", "new"); err != nil {
		t.Fatal(err)
	}
	newPr := New(p, "new")
	if !newPr.Exists() {
		t.Fatal("new profile source missing")
	}
	link, err := os.Readlink(filepath.Join(newPr.ConfigDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(newPr.SourceDir, "settings.json")
	if link != want {
		t.Errorf("symlink target = %q, want %q", link, want)
	}
}
