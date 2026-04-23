package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCreatesManifestAndPrintsShellInstruction(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)
	// Keep XDG from the outer env from polluting.
	t.Setenv("XDG_CONFIG_HOME", "")

	var stdout, stderr bytes.Buffer
	r := NewRoot()
	r.SetOut(&stdout)
	r.SetErr(&stderr)
	r.SetArgs([]string{"init", "--shell", "bash"})

	if err := r.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}

	manifestPath := filepath.Join(root, ".config", "ccp", "manifest.toml")
	info, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatalf("manifest not created: %v", err)
	}
	if info.Size() == 0 {
		t.Errorf("manifest is empty")
	}

	out := stdout.String()
	if !strings.Contains(out, "Initialized ccp") {
		t.Errorf("missing init message: %s", out)
	}
	if !strings.Contains(out, `eval "$(ccp shell-init bash)"`) {
		t.Errorf("missing shell-init hint for bash: %s", out)
	}

	// Re-running is idempotent and reports existing.
	stdout.Reset()
	r2 := NewRoot()
	r2.SetOut(&stdout)
	r2.SetArgs([]string{"init"})
	if err := r2.Execute(); err != nil {
		t.Fatalf("re-init: %v", err)
	}
	if !strings.Contains(stdout.String(), "already initialized") {
		t.Errorf("expected 'already initialized', got: %s", stdout.String())
	}
}

func TestVersionCmd(t *testing.T) {
	var stdout bytes.Buffer
	r := NewRoot()
	r.SetOut(&stdout)
	r.SetArgs([]string{"version"})
	if err := r.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), Version) {
		t.Errorf("version output missing %q: got %q", Version, stdout.String())
	}
}
