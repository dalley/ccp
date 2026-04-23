package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// initBareTestRepo creates a bare repo suitable for the sync CLI tests to
// push against. Kept local to the cli package so we don't need to export
// test helpers from internal/sync.
func initBareTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.Main},
		Bare:        true,
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestSyncSetupOnEmptyRemoteInitializes covers the "first-machine" path:
// setup against an empty remote should fall through to init + set-remote.
func TestSyncSetupOnEmptyRemoteInitializes(t *testing.T) {
	root := setupCLI(t)
	bare := initBareTestRepo(t)

	out, _, err := runCLI(t, "", "sync", "setup", "--url", bare)
	if err != nil {
		t.Fatalf("sync setup: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Sync ready") {
		t.Errorf("expected 'Sync ready' in output: %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, ".config/ccp/.ccp-sync.json")); err != nil {
		t.Errorf("marker missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".config/ccp/.gitignore")); err != nil {
		t.Errorf(".gitignore missing: %v", err)
	}
}

// TestSyncSetupRefusesNonCcpRemote covers the marker-enforcement path:
// cloning a remote without a valid .ccp-sync.json should fail, and the
// cloned content should be cleaned up so a retry is possible.
func TestSyncSetupRefusesNonCcpRemote(t *testing.T) {
	root := setupCLI(t)

	// Build a remote that has commits but NO .ccp-sync.json marker.
	remote := t.TempDir()
	if _, err := git.PlainInitWithOptions(remote, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.Main},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "README.md"), []byte("not ccp"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := git.PlainOpen(remote)
	if err != nil {
		t.Fatal(err)
	}
	w, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit("init", &git.CommitOptions{Author: testSig()}); err != nil {
		t.Fatal(err)
	}

	_, _, err = runCLI(t, "", "sync", "setup", "--url", remote)
	if err == nil {
		t.Fatal("expected error for non-ccp remote, got nil")
	}
	if !strings.Contains(err.Error(), "not a ccp-managed sync repo") {
		t.Errorf("unexpected error: %v", err)
	}
	// The cloned content should be cleaned up so a retry is possible.
	if _, err := os.Stat(filepath.Join(root, ".config/ccp/.git")); err == nil {
		t.Errorf(".git was not cleaned up after refused setup")
	}
}

// TestSyncPushRequiresSetup ensures push returns a clear error rather than
// a raw go-git error when the config dir hasn't been bonded.
func TestSyncPushRequiresSetup(t *testing.T) {
	setupCLI(t)
	_, _, err := runCLI(t, "", "sync", "push")
	if err == nil {
		t.Fatal("expected error when sync is not set up")
	}
	if !strings.Contains(err.Error(), "sync setup") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestSyncPullRefusesDirtyTree covers the non-destructive-default path:
// pull with a dirty tree should return ExitConflict via ErrDirtyWorkingTree.
func TestSyncPullRefusesDirtyTree(t *testing.T) {
	root := setupCLI(t)
	bare := initBareTestRepo(t)
	if _, _, err := runCLI(t, "", "sync", "setup", "--url", bare); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "sync", "push"); err != nil {
		t.Fatal(err)
	}
	// Dirty the working tree.
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/settings.json"),
		[]byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCLI(t, "", "sync", "pull")
	if err == nil {
		t.Fatal("expected pull refusal on dirty tree")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("unexpected error: %v", err)
	}
	if ExitCodeFor(err) != ExitConflict {
		t.Errorf("exit code = %d, want %d (conflict)", ExitCodeFor(err), ExitConflict)
	}
}

// TestSyncStatusInEachState walks through the no-repo / clean / dirty
// states and asserts the output differentiates each one.
func TestSyncStatusInEachState(t *testing.T) {
	root := setupCLI(t)

	// No repo yet.
	out, _, err := runCLI(t, "", "sync", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "not set up") {
		t.Errorf("no-repo status: %s", out)
	}

	bare := initBareTestRepo(t)
	if _, _, err := runCLI(t, "", "sync", "setup", "--url", bare); err != nil {
		t.Fatal(err)
	}

	// Clean.
	out, _, err = runCLI(t, "", "sync", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "clean") {
		t.Errorf("clean status: %s", out)
	}

	// Dirty.
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/.ccp-sync.json"),
		[]byte(`{"managedBy":"ccp","version":1,"mutated":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err = runCLI(t, "", "sync", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "uncommitted") {
		t.Errorf("dirty status: %s", out)
	}
}

// TestSyncStatusJSON asserts the --json shape round-trips cleanly.
func TestSyncStatusJSON(t *testing.T) {
	setupCLI(t)
	bare := initBareTestRepo(t)
	if _, _, err := runCLI(t, "", "sync", "setup", "--url", bare); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "sync", "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"repo_exists": true`, `"dirty": false`, `"changed_files"`, `"ahead": 0`} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON status missing %q: %s", want, out)
		}
	}
}

func testSig() *object.Signature {
	return &object.Signature{Name: "test", Email: "test@example", When: time.Now()}
}
