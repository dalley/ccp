package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewCreatesTimestampedDir(t *testing.T) {
	base := t.TempDir()
	dir, err := New(base, "pre-delete-work")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dir, base) {
		t.Errorf("dir %q not under base %q", dir, base)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("backup dir not created: %v", err)
	}
	if !strings.Contains(dir, "pre-delete-work") {
		t.Errorf("op label missing in %q", dir)
	}
}

func TestNewReturnsUniqueDirsWithinSameSecond(t *testing.T) {
	base := t.TempDir()
	seen := map[string]struct{}{}
	for i := 0; i < 10; i++ {
		dir, err := New(base, "op")
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[dir]; dup {
			t.Fatalf("collision on iteration %d: %s", i, dir)
		}
		seen[dir] = struct{}{}
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	base := t.TempDir()
	// Seed 5 backups with predictable names.
	for _, n := range []string{
		"2026-01-01T00-00-00_a",
		"2026-02-01T00-00-00_b",
		"2026-03-01T00-00-00_c",
		"2026-04-01T00-00-00_d",
		"2026-05-01T00-00-00_e",
	} {
		if err := os.MkdirAll(filepath.Join(base, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := Prune(base, 2); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(base)
	if len(entries) != 2 {
		t.Fatalf("kept %d, want 2", len(entries))
	}
	got := []string{entries[0].Name(), entries[1].Name()}
	want := []string{"2026-04-01T00-00-00_d", "2026-05-01T00-00-00_e"}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLatestReturnsMostRecent(t *testing.T) {
	base := t.TempDir()
	if got, _ := Latest(base); got != "" {
		t.Errorf("empty Latest = %q, want \"\"", got)
	}
	for _, n := range []string{"2026-01-01T00-00-00_a", "2026-05-01T00-00-00_z"} {
		_ = os.MkdirAll(filepath.Join(base, n), 0o755)
	}
	got, err := Latest(base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "2026-05-01T00-00-00_z") {
		t.Errorf("Latest = %q", got)
	}
}
