package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrationAdvisoryFiresOnceOnFirstUse verifies the v2 first-run
// migration advisory: an empty LastSeenVersion should trigger the full
// secrets advisory on first `ccp use`, and a second invocation (after the
// manifest has been stamped) should stay silent. This is the core of Key
// Technical Decision #22 — at-most-once advisory, gated by the manifest
// stamp written inside the same lock as ActiveProfile.
func TestMigrationAdvisoryFiresOnceOnFirstUse(t *testing.T) {
	setupCLI(t)

	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatalf("create: %v", err)
	}

	// First `ccp use` — manifest has no LastSeenVersion yet, so the full
	// v2 secrets advisory should land on stderr.
	_, stderr, err := runCLI(t, "", "use", "work")
	if err != nil {
		t.Fatalf("first use: %v", err)
	}
	if !strings.Contains(stderr, "v2.0 adds secrets separation") {
		t.Errorf("first-run advisory missing from stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "ccp profile audit") {
		t.Errorf("first-run advisory missing audit hint:\n%s", stderr)
	}

	// Second `ccp use` — LastSeenVersion now equals the current binary, so
	// the advisory must stay silent. A re-fire here would mean the stamp
	// didn't land or the equality check is wrong.
	_, stderr2, err := runCLI(t, "", "use", "work")
	if err != nil {
		t.Fatalf("second use: %v", err)
	}
	if strings.Contains(stderr2, "v2.0 adds secrets separation") {
		t.Errorf("advisory re-fired on second use:\n%s", stderr2)
	}
	if strings.Contains(stderr2, "upgraded from") {
		t.Errorf("general upgrade advisory fired unexpectedly:\n%s", stderr2)
	}
}

// TestUseShellIsSafeToEvalInSh stands up a CCP_ROOT whose path contains a
// `$` and a single quote and verifies that `ccp use --shell`'s output can
// be sourced in a real /bin/sh without the shell treating those bytes as
// metacharacters. Catches the regression where we'd use `%q` (Go quoting,
// which doesn't neutralise `$`) instead of a POSIX single-quote wrapper.
//
// Parallels the live-sh pattern from shellinit_test.go:TestShellInitPosixActuallyRunsInSh.
func TestUseShellIsSafeToEvalInSh(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	// t.TempDir() under a parent path with `$` and `'` — we construct it
	// by nesting under the default temp dir and naming the child with
	// shell-hostile bytes.
	base := t.TempDir()
	hostile := filepath.Join(base, "it's $weird")
	if err := os.MkdirAll(hostile, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCP_ROOT", hostile)
	t.Setenv("XDG_CONFIG_HOME", "")

	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatalf("create: %v", err)
	}
	out, _, err := runCLI(t, "", "use", "work", "--shell")
	if err != nil {
		t.Fatalf("use --shell: %v\n%s", err, out)
	}
	// Eval the output in /bin/sh and echo back the resulting env.
	script := "set -e\n" + out + "\nprintf 'DIR=%s\\n' \"$CLAUDE_CONFIG_DIR\"\nprintf 'PROF=%s\\n' \"$CCP_PROFILE\"\n"
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	got, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh eval failed: %v\nscript:\n%s\noutput:\n%s", err, script, got)
	}
	gotStr := string(got)
	// CLAUDE_CONFIG_DIR should contain the hostile path intact.
	if !strings.Contains(gotStr, "DIR="+hostile) {
		t.Errorf("CLAUDE_CONFIG_DIR mis-quoted; got:\n%s\nwanted substring: DIR=%s", gotStr, hostile)
	}
	if !strings.Contains(gotStr, "PROF=work") {
		t.Errorf("CCP_PROFILE mis-quoted; got:\n%s", gotStr)
	}
}

// TestMigrationAdvisorySuppressedOnSaveFailure verifies the
// advisory-after-save invariant: if manifest.Save fails, the new
// LastSeenVersion is not persisted, so the advisory must NOT fire —
// otherwise the user sees it, dismisses it, and then sees it again
// on the next `ccp use` because the stamp didn't land.
//
// We simulate save failure by stripping write permission from the
// config dir AFTER `ccp profile create` has set everything up. Save
// calls os.CreateTemp on filepath.Dir(path), which fails EACCES when
// the directory is read+execute-only. Skipped as root (chmod is
// advisory for euid 0).
func TestMigrationAdvisorySuppressedOnSaveFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("cannot test chmod as root")
	}
	setupCLI(t)

	// Create + first-run `use` to stamp the manifest with a known
	// version. We run the "real" advisory flow first so the test is
	// meaningful: after this, LastSeenVersion == Version, and any
	// subsequent `use` at that same Version would normally be silent.
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := runCLI(t, "", "use", "work"); err != nil {
		t.Fatalf("first use (stamp): %v", err)
	}

	// Flip Version so there IS a prior/current mismatch, meaning an
	// advisory would normally fire.
	origVersion := Version
	Version = "9.9.9"
	t.Cleanup(func() { Version = origVersion })

	// Make the manifest's parent dir read+execute only. The lock file
	// already exists (opened for rdwr, which works via file-level perms
	// on an existing file), but manifest.Save's CreateTemp on this dir
	// will fail EACCES.
	configDir := filepath.Join(os.Getenv("CCP_ROOT"), ".config", "ccp")
	if err := os.Chmod(configDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(configDir, 0o700) })

	_, stderr, err := runCLI(t, "", "use", "work")
	// use MUST fail — withLockedState surfaces save errors.
	if err == nil {
		t.Fatalf("use expected to fail when manifest.Save fails, got nil err\nstderr:\n%s", stderr)
	}
	// And the advisory MUST NOT have been emitted — that's the whole
	// point of gating it behind save success.
	if strings.Contains(stderr, "v2.0 adds secrets separation") ||
		strings.Contains(stderr, "upgraded from") {
		t.Errorf("advisory leaked despite manifest.Save failure; stderr:\n%s", stderr)
	}
}

// TestMigrationAdvisoryOnGeneralUpgrade covers the non-empty-but-different
// branch of emitMigrationAdvisory: a manifest already stamped with some
// older version should get the brief one-liner, not the verbose v2 message.
func TestMigrationAdvisoryOnGeneralUpgrade(t *testing.T) {
	// We swap in a distinct Version string, run `ccp use`, then swap
	// back. This mirrors an upgrade: the manifest was stamped by v2.0.0,
	// and the user is now running v2.1.0.
	origVersion := Version
	t.Cleanup(func() { Version = origVersion })

	setupCLI(t)

	// Stamp the manifest with an "old" version by running `ccp use` under it.
	Version = "2.0.0"
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := runCLI(t, "", "use", "work"); err != nil {
		t.Fatalf("use (stamp): %v", err)
	}

	// Now upgrade the binary and re-run.
	Version = "2.1.0"
	_, stderr, err := runCLI(t, "", "use", "work")
	if err != nil {
		t.Fatalf("use (post-upgrade): %v", err)
	}
	if !strings.Contains(stderr, "upgraded from 2.0.0 to 2.1.0") {
		t.Errorf("general upgrade advisory missing:\n%s", stderr)
	}
	// The verbose first-run message must NOT appear here; this is the
	// short-form branch.
	if strings.Contains(stderr, "v2.0 adds secrets separation") {
		t.Errorf("first-run advisory leaked into general-upgrade path:\n%s", stderr)
	}
}
