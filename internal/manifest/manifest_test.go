package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingReturnsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.toml")
	m, existed, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if existed {
		t.Errorf("existed = true, want false")
	}
	if m.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", m.SchemaVersion, CurrentSchemaVersion)
	}
	if m.ActiveProfile != "" {
		t.Errorf("ActiveProfile = %q, want empty", m.ActiveProfile)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.toml")
	m := Manifest{
		SchemaVersion: CurrentSchemaVersion,
		ActiveProfile: "work",
		DefaultShell:  "zsh",
	}
	if err := Save(path, m); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, existed, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !existed {
		t.Fatalf("existed = false, want true")
	}
	if got != m {
		t.Errorf("round trip diff: got %+v, want %+v", got, m)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.toml")
	if err := os.WriteFile(path, []byte("schema_version = 1\nactive_profile = \"old\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := Manifest{SchemaVersion: CurrentSchemaVersion, ActiveProfile: "new"}
	if err := Save(path, m); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// No orphan temp files should remain.
	for _, e := range entries {
		if e.Name() != "manifest.toml" {
			t.Errorf("unexpected leftover file %q", e.Name())
		}
	}
}

func TestLoadRejectsFutureSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.toml")
	if err := os.WriteFile(path, []byte("schema_version = 9999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(path)
	if err == nil {
		t.Fatalf("expected error for future schema, got nil")
	}
}
