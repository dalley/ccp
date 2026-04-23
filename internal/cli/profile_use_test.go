package cli

import (
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
