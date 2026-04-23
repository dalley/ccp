package paths

import (
	"path/filepath"
	"testing"
)

func TestResolveWithCCPRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", "/should/be/ignored")

	p, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.Home != root {
		t.Errorf("Home = %q, want %q", p.Home, root)
	}
	wantConfig := filepath.Join(root, ".config", "ccp")
	if p.ConfigDir != wantConfig {
		t.Errorf("ConfigDir = %q, want %q", p.ConfigDir, wantConfig)
	}
	if p.ClaudeHome != filepath.Join(root, ".claude") {
		t.Errorf("ClaudeHome = %q", p.ClaudeHome)
	}
}

func TestEnsureCreatesDirs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)

	p, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for _, d := range []string{p.ConfigDir, p.ProfilesDir, p.BackupsDir} {
		info, err := statDir(d)
		if err != nil {
			t.Fatalf("stat %s: %v", d, err)
		}
		if !info.IsDir() {
			t.Errorf("%s is not a dir", d)
		}
	}
}

func TestProfileDirs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)

	p, _ := Resolve()
	if got := p.ProfileConfigDir("work"); got != filepath.Join(root, ".claude-work") {
		t.Errorf("ProfileConfigDir = %q", got)
	}
	if got := p.ProfileSourceDir("work"); got != filepath.Join(root, ".config", "ccp", "profiles", "work") {
		t.Errorf("ProfileSourceDir = %q", got)
	}
}

func TestExpandHomeAndRelative(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)

	p, _ := Resolve()
	if got := p.ExpandHome("~/.claude"); got != filepath.Join(root, ".claude") {
		t.Errorf("ExpandHome = %q", got)
	}
	if got := p.ExpandHome("~"); got != root {
		t.Errorf("ExpandHome(~) = %q", got)
	}
	if got := p.ExpandHome("/absolute"); got != "/absolute" {
		t.Errorf("ExpandHome(/absolute) = %q", got)
	}
	if got := p.ToHomeRelative(filepath.Join(root, ".claude")); got != "~/.claude" {
		t.Errorf("ToHomeRelative = %q", got)
	}
	if got := p.ToHomeRelative(root); got != "~" {
		t.Errorf("ToHomeRelative(home) = %q", got)
	}
}
