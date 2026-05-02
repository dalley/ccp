//go:build !windows

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dalley/ccp/internal/refs"
)

// TestProfileRefreshHonoursTimeout verifies that `ccp profile refresh`
// propagates ExecRefreshTimeout into the ref resolver — a hung op read
// must not pin the command. We inject a fake opRead that blocks until the
// context fires, shrink ExecRefreshTimeout to 200ms for the duration of
// the test, and expect the command to return within a small multiple of
// that budget with an error wrapping refs.ErrSecretRefUnresolved.
//
// Guards Finding #1 from round 2 of the code review (the `BuildSymlinks`
// / `RefreshSymlinks` zero-arg variants used to call
// `context.Background()` internally, so `ccp profile refresh` on a
// profile with `{{ op://… }}` refs could wedge forever).
func TestProfileRefreshHonoursTimeout(t *testing.T) {
	setupCLI(t)

	// Create the profile first, with settings.json content that does NOT
	// contain refs — that way Create's own BuildSymlinks is a no-op on
	// refs and won't itself block. We inject the ref afterward.
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Now drop a settings.json with an op:// ref into the source tree so
	// the subsequent refresh has something to resolve.
	home := os.Getenv("CCP_ROOT")
	srcSettings := filepath.Join(home, ".config", "ccp", "profiles", "work", "settings.json")
	if err := os.WriteFile(srcSettings, []byte(`{"k":"{{ op://V/I/F }}"}`), 0o644); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}

	// OP_SERVICE_ACCOUNT_TOKEN bypasses the isTTY guard so resolveOp
	// proceeds to the opRead hook (which we override below).
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "ops_fake")

	// Blocking opRead: returns only when ctx fires. Mirrors a real hung
	// `op` process (network stall, 1Password service down).
	refs.SetOpRead(func(ctx context.Context, ref string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	t.Cleanup(func() { refs.SetOpRead(nil) })

	// Shrink the refresh budget so the test runs fast. The 200ms target
	// in the review plan balances "well below a real op round-trip" with
	// "enough slack for goroutine scheduling jitter on loaded CI."
	orig := ExecRefreshTimeout
	ExecRefreshTimeout = 200 * time.Millisecond
	t.Cleanup(func() { ExecRefreshTimeout = orig })

	start := time.Now()
	_, _, err := runCLI(t, "", "profile", "refresh", "work")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("refresh with hung opRead returned nil error; want timeout")
	}
	// Either the underlying context deadline or the refs sentinel should
	// surface. The render path wraps timeouts as ErrSecretRefUnresolved.
	if !errors.Is(err, refs.ErrSecretRefUnresolved) && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want wrapping ErrSecretRefUnresolved or DeadlineExceeded", err)
	}
	// Budget check: give a generous 5s ceiling so flaky CI doesn't fail
	// the test for load-related jitter, while still catching a regression
	// back to unbounded waits. 5s is three orders of magnitude below the
	// 30s default and a hundred times the 200ms test budget.
	if elapsed > 5*time.Second {
		t.Errorf("refresh took %v; expected < 5s with 200ms timeout (wedged?)", elapsed)
	}
}
