//go:build !windows

package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dalley/ccp/internal/allowlist"
	"github.com/dalley/ccp/internal/paths"
)

// chdirTo changes to dir and registers cleanup that restores the prior cwd.
// Uses t.Chdir (Go 1.24+) — the repo's go.mod pins Go 1.26, so this is safe.
func chdirTo(t *testing.T, dir string) {
	t.Helper()
	t.Chdir(dir)
}

// writeMarkerAllow is the allow_test twin of shell_resolve_dir_test's
// writeMarker. We duplicate it to keep allow_test self-contained and to
// avoid cross-file coupling if shell_resolve_dir_test.go is refactored.
func writeMarkerAllow(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAllowApprovesNearestMarker covers the happy path: a marker exists
// in CWD, `ccp allow` approves it and prints the profile name + abbreviated
// hash, and a following `ccp allow --status` reports Allowed.
func TestAllowApprovesNearestMarker(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarkerAllow(t, marker, "work\n")
	chdirTo(t, repo)

	out, _, err := runCLI(t, "", "allow")
	if err != nil {
		t.Fatalf("allow: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Approved") {
		t.Errorf("output missing 'Approved': %q", out)
	}
	if !strings.Contains(out, `profile "work"`) {
		t.Errorf("output missing profile name: %q", out)
	}
	if !strings.Contains(out, "sha256:") {
		t.Errorf("output missing sha256 prefix: %q", out)
	}
	if !strings.Contains(out, marker) {
		t.Errorf("output missing marker path: %q", out)
	}

	// --status reports Allowed.
	out, _, err = runCLI(t, "", "allow", "--status")
	if err != nil {
		t.Fatalf("allow --status: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "Allowed ") {
		t.Errorf("status output should start with 'Allowed ': %q", out)
	}
	if !strings.Contains(out, `profile "work"`) {
		t.Errorf("status missing profile name: %q", out)
	}
}

// TestAllowStatusHashMismatch verifies that editing the marker after
// approval produces HashMismatch and an ExitConflict return code.
func TestAllowStatusHashMismatch(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarkerAllow(t, marker, "work\n")
	chdirTo(t, repo)

	if _, _, err := runCLI(t, "", "allow"); err != nil {
		t.Fatalf("allow: %v", err)
	}

	// Same valid grammar, different bytes.
	writeMarkerAllow(t, marker, "personal\n")

	out, _, err := runCLI(t, "", "allow", "--status")
	if err == nil {
		t.Fatal("expected error on hash mismatch")
	}
	if !errors.Is(err, allowlist.ErrMarkerHashMismatch) {
		t.Errorf("expected ErrMarkerHashMismatch, got %v", err)
	}
	if ExitCodeFor(err) != ExitConflict {
		t.Errorf("exit code = %d, want ExitConflict (%d)", ExitCodeFor(err), ExitConflict)
	}
	if !strings.HasPrefix(out, "HashMismatch ") {
		t.Errorf("output should start with 'HashMismatch ': %q", out)
	}
	if !strings.Contains(out, "was sha256:") || !strings.Contains(out, "now sha256:") {
		t.Errorf("output should show was/now hashes: %q", out)
	}
}

// TestDenyRemovesEntryAndStatusReportsUnallowed walks through the full
// lifecycle: approve, deny, status. After deny, status should be Unallowed
// with ExitConflict.
func TestDenyRemovesEntryAndStatusReportsUnallowed(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarkerAllow(t, marker, "work\n")
	chdirTo(t, repo)

	if _, _, err := runCLI(t, "", "allow"); err != nil {
		t.Fatalf("allow: %v", err)
	}

	out, _, err := runCLI(t, "", "deny")
	if err != nil {
		t.Fatalf("deny: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "Revoked ") {
		t.Errorf("deny output should start with 'Revoked ': %q", out)
	}

	out, _, err = runCLI(t, "", "allow", "--status")
	if err == nil {
		t.Fatal("expected ErrMarkerNotAllowed on post-deny status")
	}
	if !errors.Is(err, allowlist.ErrMarkerNotAllowed) {
		t.Errorf("expected ErrMarkerNotAllowed, got %v", err)
	}
	if ExitCodeFor(err) != ExitConflict {
		t.Errorf("exit code = %d, want ExitConflict", ExitCodeFor(err))
	}
	if !strings.HasPrefix(out, "Unallowed ") {
		t.Errorf("output should start with 'Unallowed ': %q", out)
	}
}

// TestDenyOnUnapprovedMarkerIsNoOp: `ccp deny` on a marker that was never
// approved exits 0 and prints a clear "nothing to do" message.
func TestDenyOnUnapprovedMarkerIsNoOp(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarkerAllow(t, marker, "work\n")
	chdirTo(t, repo)

	out, _, err := runCLI(t, "", "deny")
	if err != nil {
		t.Fatalf("deny: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "No entry to revoke for ") {
		t.Errorf("deny output should indicate no-op: %q", out)
	}
}

// TestAllowNoMarkerErrors asserts that `ccp allow` without a marker
// anywhere up the walk errors with a clear hint, while `--status` quietly
// reports "no marker" with exit 0.
func TestAllowNoMarkerErrors(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	chdirTo(t, repo)

	_, _, err := runCLI(t, "", "allow")
	if err == nil {
		t.Fatal("expected error when no marker is found")
	}
	if !strings.Contains(err.Error(), "no .claude-profile found") {
		t.Errorf("error message should explain: %v", err)
	}
}

// TestAllowStatusNoMarkerExitZero verifies `--status` stays silent & exit 0
// when there's no marker at all (fresh-machine friendly).
func TestAllowStatusNoMarkerExitZero(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	chdirTo(t, repo)

	out, _, err := runCLI(t, "", "allow", "--status")
	if err != nil {
		t.Fatalf("allow --status: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "No .claude-profile found") {
		t.Errorf("output should report 'No .claude-profile found': %q", out)
	}
}

// TestDenyNoMarkerNoOp: `ccp deny` without a marker in any ancestor is a
// plain no-op that exits 0.
func TestDenyNoMarkerNoOp(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	chdirTo(t, repo)

	out, _, err := runCLI(t, "", "deny")
	if err != nil {
		t.Fatalf("deny: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "No .claude-profile found") {
		t.Errorf("output: %q", out)
	}
}

// TestAllowIdempotent: approving a marker twice back-to-back must succeed
// and leave the same hash on disk. Re-approval is the natural recovery path
// after a HashMismatch, so it absolutely must be idempotent.
func TestAllowIdempotent(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarkerAllow(t, marker, "work\n")
	chdirTo(t, repo)

	if _, _, err := runCLI(t, "", "allow"); err != nil {
		t.Fatalf("allow #1: %v", err)
	}
	if _, _, err := runCLI(t, "", "allow"); err != nil {
		t.Fatalf("allow #2: %v", err)
	}

	// Inspect the allow-list directly — one entry, one hash.
	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	f, _, err := allowlist.Load(p.AllowlistPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 1 {
		t.Errorf("expected 1 entry, got %d: %v", len(f.Entries), f.Entries)
	}
}

// TestAllowStatusJSON verifies the --json emission in all three non-error
// statuses: allowed, unallowed, hash_mismatch. The no_marker case is
// covered separately because it has a different schema slice.
func TestAllowStatusJSON(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarkerAllow(t, marker, "work\n")
	chdirTo(t, repo)

	// Unallowed first.
	out, _, err := runCLI(t, "", "allow", "--status", "--json")
	if err == nil {
		t.Fatal("expected conflict error for unallowed marker")
	}
	r := decodeStatus(t, out)
	if r.Status != "unallowed" {
		t.Errorf("status = %q, want unallowed", r.Status)
	}
	if r.Marker != marker {
		t.Errorf("marker = %q, want %q", r.Marker, marker)
	}
	if r.CurrentHash == "" {
		t.Errorf("current_hash should be populated even when unallowed")
	}

	// Approve → allowed.
	if _, _, err := runCLI(t, "", "allow"); err != nil {
		t.Fatalf("allow: %v", err)
	}
	out, _, err = runCLI(t, "", "allow", "--status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v\n%s", err, out)
	}
	r = decodeStatus(t, out)
	if r.Status != "allowed" {
		t.Errorf("status = %q, want allowed", r.Status)
	}
	if r.Profile != "work" {
		t.Errorf("profile = %q, want work", r.Profile)
	}
	if r.ApprovedHash == "" || r.CurrentHash == "" {
		t.Errorf("approved/current hash should both be populated: %+v", r)
	}
	if r.ApprovedHash != r.CurrentHash {
		t.Errorf("approved (%s) != current (%s) in allowed state", r.ApprovedHash, r.CurrentHash)
	}

	// Edit → hash_mismatch.
	writeMarkerAllow(t, marker, "personal\n")
	out, _, err = runCLI(t, "", "allow", "--status", "--json")
	if err == nil {
		t.Fatal("expected conflict error for hash mismatch")
	}
	r = decodeStatus(t, out)
	if r.Status != "hash_mismatch" {
		t.Errorf("status = %q, want hash_mismatch", r.Status)
	}
	if r.ApprovedHash == r.CurrentHash {
		t.Errorf("approved/current hashes should differ in mismatch state: %+v", r)
	}
}

// TestAllowStatusJSONNoMarker: --json still emits well-formed output when
// no marker exists, so scripts don't have to special-case exit 0 + no output.
func TestAllowStatusJSONNoMarker(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	chdirTo(t, repo)

	out, _, err := runCLI(t, "", "allow", "--status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v\n%s", err, out)
	}
	r := decodeStatus(t, out)
	if r.Status != "no_marker" {
		t.Errorf("status = %q, want no_marker", r.Status)
	}
	if r.Marker != "" {
		t.Errorf("marker = %q, want empty", r.Marker)
	}
}

// TestAllowSymlinkedMarkerRefused verifies the hash layer refuses to
// follow a symlinked marker, and `ccp allow` surfaces the error rather
// than silently approving.
func TestAllowSymlinkedMarkerRefused(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	target := filepath.Join(root, "elsewhere", ".claude-profile-source")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarkerAllow(t, target, "work\n")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, marker); err != nil {
		t.Fatal(err)
	}
	chdirTo(t, repo)

	_, _, err := runCLI(t, "", "allow")
	if err == nil {
		t.Fatal("expected error on symlinked marker")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error should mention symlink: %v", err)
	}
}

// TestAllowMalformedMarkerByteOffset covers the error-message contract:
// a BOM-prefixed marker must produce an error naming byte offset 0.
func TestAllowMalformedMarkerByteOffset(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	// UTF-8 BOM + "work\n".
	writeMarkerAllow(t, marker, "\xEF\xBB\xBFwork\n")
	chdirTo(t, repo)

	_, _, err := runCLI(t, "", "allow")
	if err == nil {
		t.Fatal("expected error on BOM-prefixed marker")
	}
	if !errors.Is(err, allowlist.ErrInvalidMarker) {
		t.Errorf("expected ErrInvalidMarker, got %v", err)
	}
	if !strings.Contains(err.Error(), "byte offset 0") {
		t.Errorf("error should name byte offset 0: %v", err)
	}
}

// TestAllowStatusMalformedMarkerSurfaced: `--status` on a malformed marker
// returns the ErrInvalidMarker verbatim so the user can see the byte-offset
// hint the allowlist package produced.
func TestAllowStatusMalformedMarkerSurfaced(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	// CRLF.
	writeMarkerAllow(t, marker, "work\r\n")
	chdirTo(t, repo)

	_, _, err := runCLI(t, "", "allow", "--status")
	if err == nil {
		t.Fatal("expected error on CRLF marker")
	}
	if !errors.Is(err, allowlist.ErrInvalidMarker) {
		t.Errorf("expected ErrInvalidMarker, got %v", err)
	}
	if !strings.Contains(err.Error(), "carriage return") {
		t.Errorf("error should name carriage return: %v", err)
	}
}

// TestAllowThenShellResolveDirAgrees is the Unit-10 interaction regression:
// after `ccp allow`, the hidden `shell-resolve-dir` hot path sees the same
// marker as Allowed and emits the CCP_AUTO_PROFILE line.
func TestAllowThenShellResolveDirAgrees(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarkerAllow(t, marker, "work\n")
	chdirTo(t, repo)

	if _, _, err := runCLI(t, "", "allow"); err != nil {
		t.Fatalf("allow: %v", err)
	}
	out, _, err := runCLI(t, "", "shell-resolve-dir", repo)
	if err != nil {
		t.Fatalf("shell-resolve-dir: %v\n%s", err, out)
	}
	if !strings.Contains(out, "CCP_AUTO_PROFILE='work'") {
		t.Errorf("shell-resolve-dir should see 'work' profile after allow:\n%s", out)
	}
	if !strings.Contains(out, "CCP_AUTO_MARKER='"+marker+"'") {
		t.Errorf("shell-resolve-dir should emit the marker path:\n%s", out)
	}
}

// TestAllowWalksUpFromSubdir exercises FindMarker integration: running
// `ccp allow` from a nested sub-directory should still find and approve
// the marker in the repo root.
func TestAllowWalksUpFromSubdir(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	sub := filepath.Join(repo, "src", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(repo, ".claude-profile")
	writeMarkerAllow(t, marker, "work\n")
	chdirTo(t, sub)

	out, _, err := runCLI(t, "", "allow")
	if err != nil {
		t.Fatalf("allow from subdir: %v\n%s", err, out)
	}
	if !strings.Contains(out, marker) {
		t.Errorf("walk-up didn't find ancestor marker, got:\n%s", out)
	}
}

// TestAllowRejectsJSONWithoutStatus keeps --json locked to --status so a
// future extension doesn't silently drop the flag.
func TestAllowRejectsJSONWithoutStatus(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarkerAllow(t, marker, "work\n")
	chdirTo(t, repo)

	_, _, err := runCLI(t, "", "allow", "--json")
	if err == nil {
		t.Fatal("expected error for --json without --status")
	}
	if !strings.Contains(err.Error(), "--json is only valid with --status") {
		t.Errorf("error message mismatch: %v", err)
	}
}

// decodeStatus parses the --json emission of `ccp allow --status`.
// Extracted as a helper so test bodies stay focused on assertions.
func decodeStatus(t *testing.T, out string) statusReport {
	t.Helper()
	var r statusReport
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("json decode failed: %v\nbody: %q", err, out)
	}
	return r
}
