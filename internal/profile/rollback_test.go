package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRollbackRestoresDeletedProfile(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	if err := os.WriteFile(filepath.Join(pr.SourceDir, "settings.json"), []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	bkDir := filepath.Join(p.BackupsDir, "pre-delete-work")
	if _, err := Delete(p, "work", bkDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pr.SourceDir); !os.IsNotExist(err) {
		t.Fatalf("delete didn't remove source: %v", err)
	}

	restored, err := Rollback(p, bkDir)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if len(restored) != 1 || restored[0] != "work" {
		t.Errorf("restored = %v, want [work]", restored)
	}
	b, err := os.ReadFile(filepath.Join(pr.SourceDir, "settings.json"))
	if err != nil || string(b) != "orig" {
		t.Errorf("restore content = %q, err = %v", b, err)
	}
}

func TestRollbackRefusesIfNameClashes(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	bkDir := filepath.Join(p.BackupsDir, "pre-delete-work")
	if _, err := Delete(p, "work", bkDir); err != nil {
		t.Fatal(err)
	}
	// Re-create a profile with the same name before rollback.
	if _, err := Create(p, "work", CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err := Rollback(p, bkDir)
	if err == nil {
		t.Fatal("expected error on name collision")
	}
	// Original source should still exist from the re-create.
	if _, err := os.Stat(pr.SourceDir); err != nil {
		t.Errorf("live profile disturbed: %v", err)
	}
}
