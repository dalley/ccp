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

// TestDoctorRenderedFileNoWarnWhenSourceHasRefs — a regular file in
// the runtime dir is expected (not a warn) when the source declares
// secret references.
func TestDoctorRenderedFileNoWarnWhenSourceHasRefs(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	src := filepath.Join(pr.SourceDir, "settings.json")
	if err := os.WriteFile(src, []byte(`{{ env.X }}`), 0o644); err != nil {
		t.Fatal(err)
	}
	installStubResolver(t, &stubResolver{env: map[string]string{"X": "v"}})
	if err := pr.BuildSymlinks(); err != nil {
		t.Fatal(err)
	}

	findings, err := Doctor(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if contains(f.Message, "regular file in runtime dir") {
			t.Errorf("rendered file wrongly flagged: %+v", f)
		}
	}
}

// TestDoctorFlagsPlainRegularFileWithoutRefs — the Claude-overwrite
// warning is preserved for sources that DON'T declare refs.
func TestDoctorFlagsPlainRegularFileWithoutRefs(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	src := filepath.Join(pr.SourceDir, "settings.json")
	if err := os.WriteFile(src, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pr.BuildSymlinks(); err != nil {
		t.Fatal(err)
	}
	// Replace the runtime symlink with a regular file to simulate a
	// Claude overwrite.
	runtimeDst := filepath.Join(pr.ConfigDir, "settings.json")
	if err := os.Remove(runtimeDst); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeDst, []byte(`{"mutated":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := Doctor(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range findings {
		if f.Severity == "warn" && contains(f.Message, "regular file in runtime dir") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected Claude-overwrite warn, got %+v", findings)
	}
}

// TestDoctorFlagsMissingKeychainEntry — an unresolved keychain ref
// surfaces as a warn finding with an actionable hint.
func TestDoctorFlagsMissingKeychainEntry(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	src := filepath.Join(pr.SourceDir, "settings.json")
	if err := os.WriteFile(src, []byte(`{{ keychain:API_KEY }}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Override the package-level hook with a miss.
	prev := KeychainLookup
	KeychainLookup = func(prof, key string) (string, error) {
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { KeychainLookup = prev })

	// Use the stub so BuildSymlinks succeeds — we only care about
	// doctor's dry-run pass.
	installStubResolver(t, &stubResolver{keychain: map[string]string{"API_KEY": "seed"}})
	if err := pr.BuildSymlinks(); err != nil {
		t.Fatal(err)
	}

	findings, err := Doctor(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range findings {
		if f.Severity == "warn" && contains(f.Message, "API_KEY") && contains(f.Hint, "ccp secret set") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected missing-keychain warn, got %+v", findings)
	}
}

// TestDoctorFlagsUnsetEnvRef — env references surface as warns when
// the variable isn't set in the process env.
func TestDoctorFlagsUnsetEnvRef(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	src := filepath.Join(pr.SourceDir, "settings.json")
	if err := os.WriteFile(src, []byte(`{{ env.CCP_DOCTOR_UNSET_TEST_VAR }}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Ensure the var is unset.
	os.Unsetenv("CCP_DOCTOR_UNSET_TEST_VAR")
	installStubResolver(t, &stubResolver{env: map[string]string{"CCP_DOCTOR_UNSET_TEST_VAR": "x"}})
	if err := pr.BuildSymlinks(); err != nil {
		t.Fatal(err)
	}

	findings, err := Doctor(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range findings {
		if f.Severity == "warn" && contains(f.Message, "CCP_DOCTOR_UNSET_TEST_VAR") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected env-unset warn, got %+v", findings)
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
