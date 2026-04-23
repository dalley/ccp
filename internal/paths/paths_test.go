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
	for _, d := range []string{p.ConfigDir, p.ProfilesDir, p.BackupsDir, p.SecretsDir, p.RuntimeManifestDir} {
		info, err := statDir(d)
		if err != nil {
			t.Fatalf("stat %s: %v", d, err)
		}
		if !info.IsDir() {
			t.Errorf("%s is not a dir", d)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("%s has mode %v, want 0700", d, info.Mode().Perm())
		}
	}
}

func TestSecretAndAllowlistPaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)

	p, _ := Resolve()
	wantSecrets := filepath.Join(root, ".config", "ccp", "secrets")
	if p.SecretsDir != wantSecrets {
		t.Errorf("SecretsDir = %q, want %q", p.SecretsDir, wantSecrets)
	}
	wantSecretFile := filepath.Join(wantSecrets, "work.json")
	if got := p.SecretFilePath("work"); got != wantSecretFile {
		t.Errorf("SecretFilePath(work) = %q, want %q", got, wantSecretFile)
	}
	wantAllowlist := filepath.Join(root, ".config", "ccp", "allowlist.toml")
	if p.AllowlistPath != wantAllowlist {
		t.Errorf("AllowlistPath = %q, want %q", p.AllowlistPath, wantAllowlist)
	}
	wantRM := filepath.Join(root, ".config", "ccp", "runtime-manifest")
	if p.RuntimeManifestDir != wantRM {
		t.Errorf("RuntimeManifestDir = %q, want %q", p.RuntimeManifestDir, wantRM)
	}
	wantRMFile := filepath.Join(wantRM, "work.json")
	if got := p.RuntimeManifestPath("work"); got != wantRMFile {
		t.Errorf("RuntimeManifestPath(work) = %q, want %q", got, wantRMFile)
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
