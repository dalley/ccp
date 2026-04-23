package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dalley/ccp/internal/profile"
)

// TestPathTraversalNamesAreRejectedAtCLI asserts that name-accepting commands
// refuse a profile name that would escape the profiles directory. Regression
// test for SEC-004 / ADV-001: before the fix, a name of "../../Downloads"
// reached profile.New unchecked and ccp happily resolved it against a real
// directory outside ~/.config/ccp/profiles.
func TestPathTraversalNamesAreRejectedAtCLI(t *testing.T) {
	setupCLI(t)

	for _, bad := range []string{"../evil", "../../Downloads", ".dotfile", "Work"} {
		for _, cmd := range [][]string{
			{"profile", "delete", bad, "--yes"},
			{"profile", "show", bad},
			{"profile", "refresh", bad},
			{"use", bad},
			{"exec", bad, "--", "/bin/true"},
		} {
			_, _, err := runCLI(t, "", cmd...)
			if err == nil {
				t.Errorf("%q %v: expected error for bad name, got nil", bad, cmd)
				continue
			}
			if !strings.Contains(err.Error(), "invalid profile name") &&
				!strings.Contains(err.Error(), "not found") {
				t.Errorf("%q %v: error %q does not mention validation", bad, cmd, err)
			}
		}
	}
}

// TestSymlinkEscapeRejectedByCopyTree asserts that copyTree refuses to copy a
// symlink whose target resolves outside the source tree. Regression test
// for SEC-001.
func TestSymlinkEscapeRejectedByCopyTree(t *testing.T) {
	setupCLI(t)

	// Build a fake ~/.claude/ that points settings.json at /etc/hosts.
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)
	claude := filepath.Join(root, ".claude")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(claude, "settings.json")); err != nil {
		t.Fatal(err)
	}

	_, _, err := runCLI(t, "", "profile", "create", "work", "--from-current")
	if err == nil {
		t.Fatalf("expected error creating profile with escaping symlink, got nil")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestConfirmRefusesNonInteractiveStdinWithoutYes asserts that `profile
// delete` (without --yes) errors out when stdin is not a TTY instead of
// silently "Cancelling". Regression test for CLI-001.
//
// The test exploits the fact that test binaries run with piped stdin — so
// IsTerminal returns false and we should hit the guard.
func TestConfirmRefusesNonInteractiveStdinWithoutYes(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	// Note: no --yes, no interactive stdin.
	_, _, err := runCLI(t, "", "profile", "delete", "work")
	if err == nil {
		t.Fatal("expected error without --yes on non-interactive stdin")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error should mention --yes: %v", err)
	}
	// Profile source dir should still exist — we refused rather than deleted.
	if _, err := os.Stat(filepath.Join(root, ".config/ccp/profiles/work")); err != nil {
		t.Errorf("profile was deleted despite refusal: %v", err)
	}
	_ = profile.ValidateName // silence unused import if refactored later
}
