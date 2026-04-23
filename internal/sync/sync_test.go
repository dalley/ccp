package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dalley/ccp/internal/paths"
)

// newHome seeds a tempdir that looks like $HOME with ccp's dirs in place.
func newHome(t *testing.T) (paths.Paths, func()) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", "")
	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	return p, func() {}
}

func TestInitRepoMakesInitialCommit(t *testing.T) {
	p, _ := newHome(t)
	// Seed a profile directory so there's something to commit beyond the
	// marker + gitignore.
	if err := os.MkdirAll(filepath.Join(p.ProfilesDir, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.ProfilesDir, "work", "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InitRepo(p.ConfigDir); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	ok, err := IsSyncRepo(p.ConfigDir)
	if err != nil || !ok {
		t.Fatalf("IsSyncRepo = %v, %v", ok, err)
	}
	// Marker + gitignore must exist.
	for _, name := range []string{".gitignore", SyncMarkerFilename} {
		if _, err := os.Stat(filepath.Join(p.ConfigDir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	// manifest.toml, if created later, must be gitignored — we don't need
	// a full test, just verify the ignore file lists it.
	b, _ := os.ReadFile(filepath.Join(p.ConfigDir, ".gitignore"))
	for _, want := range []string{"manifest.toml", "backups/", "lock", "secrets/"} {
		if !strings.Contains(string(b), want) {
			t.Errorf(".gitignore missing %q", want)
		}
	}
}

func TestReadMarkerRoundTrip(t *testing.T) {
	p, _ := newHome(t)
	if err := WriteMarker(p.ConfigDir); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMarker(p.ConfigDir)
	if err != nil || m == nil {
		t.Fatalf("ReadMarker = %v, %v", m, err)
	}
	if m.ManagedBy != "ccp" {
		t.Errorf("ManagedBy = %q", m.ManagedBy)
	}
	if m.Version != currentMarkerVersion {
		t.Errorf("Version = %d", m.Version)
	}
}

func TestStageAndCommitIsNoopWhenClean(t *testing.T) {
	p, _ := newHome(t)
	if err := InitRepo(p.ConfigDir); err != nil {
		t.Fatal(err)
	}
	changed, err := StageAndCommit(p.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Errorf("StageAndCommit should be a no-op on clean tree")
	}
}

func TestStageAndCommitCapturesNewFile(t *testing.T) {
	p, _ := newHome(t)
	if err := InitRepo(p.ConfigDir); err != nil {
		t.Fatal(err)
	}
	// Drop a new profile file.
	newFile := filepath.Join(p.ProfilesDir, "work", "settings.json")
	if err := os.MkdirAll(filepath.Dir(newFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := StageAndCommit(p.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Errorf("expected StageAndCommit to report changed")
	}
}

func TestCrossMachineCloneRoundTrip(t *testing.T) {
	// Bare repo shared between two "machines".
	bare := t.TempDir()
	if err := initBare(bare); err != nil {
		t.Fatal(err)
	}

	// Machine A: set up, create a profile, push.
	rootA := t.TempDir()
	t.Setenv("CCP_ROOT", rootA)
	t.Setenv("XDG_CONFIG_HOME", "")
	pa, _ := paths.Resolve()
	if err := pa.Ensure(); err != nil {
		t.Fatal(err)
	}
	workA := filepath.Join(pa.ProfilesDir, "work")
	if err := os.MkdirAll(workA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workA, "settings.json"), []byte(`{"from":"A"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InitRepo(pa.ConfigDir); err != nil {
		t.Fatalf("A init: %v", err)
	}
	if err := SetRemote(pa.ConfigDir, bare); err != nil {
		t.Fatalf("A set remote: %v", err)
	}
	if _, err := StageAndCommit(pa.ConfigDir); err != nil {
		t.Fatalf("A stage+commit: %v", err)
	}
	if err := Push(pa.ConfigDir); err != nil {
		t.Fatalf("A push: %v", err)
	}

	// Machine B: different username / HOME. Clone into a fresh configDir.
	rootB := t.TempDir()
	t.Setenv("CCP_ROOT", rootB)
	pb, _ := paths.Resolve()
	if err := pb.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := CloneOrOpen(pb.ConfigDir, bare); err != nil {
		t.Fatalf("B clone: %v", err)
	}
	// Profile should have landed with the right content.
	got, err := os.ReadFile(filepath.Join(pb.ProfilesDir, "work", "settings.json"))
	if err != nil {
		t.Fatalf("read settings on B: %v", err)
	}
	if string(got) != `{"from":"A"}` {
		t.Errorf("cross-machine content = %q, want %q", got, `{"from":"A"}`)
	}
	// Marker survived.
	m, err := ReadMarker(pb.ConfigDir)
	if err != nil || m == nil {
		t.Errorf("marker missing on B: %v %v", m, err)
	}
}

func TestPullRefusesDirtyWorkingTreeWithoutForce(t *testing.T) {
	p, _ := newHome(t)
	if err := InitRepo(p.ConfigDir); err != nil {
		t.Fatal(err)
	}
	// Dirty the tree.
	f := filepath.Join(p.ConfigDir, SyncMarkerFilename)
	if err := os.WriteFile(f, []byte(`{"managedBy":"something-else"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Pull(p.ConfigDir, PullOptions{})
	if err == nil {
		t.Fatal("expected error on dirty tree without --force")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("unexpected error: %v", err)
	}
}
