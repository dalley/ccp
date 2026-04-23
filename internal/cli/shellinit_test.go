package cli

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/dalley/ccp/internal/paths"
)

func TestShellInitPosixContainsMarkersAndGuard(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)
	p, _ := paths.Resolve()

	var buf bytes.Buffer
	if err := writeShellInit(&buf, "zsh", p); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		shellInitBegin, shellInitEnd,
		`[ -n "$CLAUDE_CONFIG_DIR" ] && return 0`,
		`export CLAUDE_CONFIG_DIR="$HOME/.claude-$profile"`,
		`ccp shell-active`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("snippet missing %q. full output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "awk") {
		t.Errorf("snippet still uses awk; should go via ccp shell-active:\n%s", out)
	}
}

func TestShellInitFishContainsSetGX(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)
	p, _ := paths.Resolve()

	var buf bytes.Buffer
	if err := writeShellInit(&buf, "fish", p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `set -gx CLAUDE_CONFIG_DIR`) {
		t.Errorf("fish snippet missing set -gx, got:\n%s", buf.String())
	}
}

func TestShellInitUnsupportedShell(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)
	p, _ := paths.Resolve()

	var buf bytes.Buffer
	err := writeShellInit(&buf, "powershell", p)
	if err == nil {
		t.Fatal("expected error for unsupported shell")
	}
}

// TestShellInitPosixActuallyRunsInSh sources the snippet in a real /bin/sh
// and verifies it sets CLAUDE_CONFIG_DIR given CCP_PROFILE. This catches
// quoting bugs the static-string checks miss.
func TestShellInitPosixActuallyRunsInSh(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)
	p, _ := paths.Resolve()

	var buf bytes.Buffer
	if err := writeShellInit(&buf, "zsh", p); err != nil {
		t.Fatal(err)
	}

	script := "set -e\nunset CLAUDE_CONFIG_DIR\nexport CCP_PROFILE=work\n" + buf.String() + "\nprintf '%s\\n' \"$CLAUDE_CONFIG_DIR\"\n"
	cmd := exec.Command("sh", "-c", script)
	// Force HOME to a known value so we can assert.
	cmd.Env = append(cmd.Env, "HOME=/tmp/fake-home", "PATH=/usr/bin:/bin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh eval failed: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	if got != "/tmp/fake-home/.claude-work" {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want %q\nfull output:\n%s", got, "/tmp/fake-home/.claude-work", out)
	}
}
