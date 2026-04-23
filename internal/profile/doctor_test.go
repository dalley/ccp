package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDoctorHealthyProfileHasNoFindings(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	if err := os.WriteFile(filepath.Join(pr.SourceDir, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pr.BuildSymlinks(); err != nil {
		t.Fatal(err)
	}
	findings, err := Doctor(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("healthy profile produced findings: %+v", findings)
	}
}

func TestDoctorMissingRuntimeDirIsWarning(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	if err := os.RemoveAll(pr.ConfigDir); err != nil {
		t.Fatal(err)
	}
	findings, err := Doctor(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 || findings[0].Severity != "warn" {
		t.Errorf("want 1 warn, got %+v", findings)
	}
}

func TestDoctorDetectsDanglingSymlink(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	src := filepath.Join(pr.SourceDir, "settings.json")
	if err := os.WriteFile(src, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pr.BuildSymlinks(); err != nil {
		t.Fatal(err)
	}
	// Nuke the source after the symlink was created.
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	findings, err := Doctor(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range findings {
		if f.Severity == "error" && contains(f.Message, "dangling symlink") {
			found = true
		}
	}
	if !found {
		t.Errorf("dangling symlink not reported: %+v", findings)
	}
}

func TestDoctorUnknownProfile(t *testing.T) {
	p := setupHome(t)
	if _, err := Doctor(p, "nope"); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(s) > 0 && (stringContains(s, sub))))
}

// stringContains avoids importing strings in this tiny file by reusing
// the standard-library Index function via the local name.
func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
