package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiffIdenticalProfiles(t *testing.T) {
	p := setupHome(t)
	a, _ := Create(p, "a", CreateOptions{})
	b, _ := Create(p, "b", CreateOptions{})
	// Seed identical content into both.
	for _, pr := range []Profile{a, b} {
		if err := os.WriteFile(filepath.Join(pr.SourceDir, "settings.json"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := Diff(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("identical profiles produced %d entries: %+v", len(entries), entries)
	}
}

func TestDiffFindsOnlyInAAndChanged(t *testing.T) {
	p := setupHome(t)
	a, _ := Create(p, "a", CreateOptions{})
	b, _ := Create(p, "b", CreateOptions{})

	if err := os.WriteFile(filepath.Join(a.SourceDir, "settings.json"), []byte("left"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.SourceDir, "settings.json"), []byte("right"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Only in A.
	if err := os.WriteFile(filepath.Join(a.SourceDir, "extra.md"), []byte("solo"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Only in B.
	if err := os.MkdirAll(filepath.Join(b.SourceDir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.SourceDir, "agents", "greet.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := Diff(a, b)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]DiffEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	if got := byPath["settings.json"].Kind; got != DiffChanged {
		t.Errorf("settings.json Kind = %q, want changed", got)
	}
	if got := byPath["extra.md"].Kind; got != DiffOnlyInA {
		t.Errorf("extra.md Kind = %q, want only-in-a", got)
	}
	if got := byPath[filepath.Join("agents", "greet.md")].Kind; got != DiffOnlyInB {
		t.Errorf("agents/greet.md Kind = %q, want only-in-b", got)
	}
}
