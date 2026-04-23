package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dalley/ccp/internal/profile"
)

// TestShowJSONRoundTrips asserts the `--json` output of `profile show`
// parses into the expected struct shape and reports every SharedItems
// entry.
func TestShowJSONRoundTrips(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/settings.json"),
		[]byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "use", "work"); err != nil {
		t.Fatal(err)
	}

	out, _, err := runCLI(t, "", "profile", "show", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rep showReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("unmarshal: %v\nout: %s", err, out)
	}
	if rep.Name != "work" || !rep.Active {
		t.Errorf("unexpected rep: %+v", rep)
	}
	if rep.Items["settings.json"] != "present" {
		t.Errorf("settings.json status = %q, want present", rep.Items["settings.json"])
	}
	if rep.Items["skills"] != "absent" {
		t.Errorf("skills status = %q, want absent", rep.Items["skills"])
	}
}

// TestDiffJSONRoundTrips asserts that the --json array of DiffEntry values
// parses cleanly and carries the correct kind + paths.
func TestDiffJSONRoundTrips(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "profile", "create", "b"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/a/settings.json"),
		[]byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/b/settings.json"),
		[]byte("B"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "profile", "diff", "a", "b", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []profile.DiffEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("unmarshal: %v\nout: %s", err, out)
	}
	if len(entries) != 1 || entries[0].Kind != profile.DiffChanged || entries[0].Path != "settings.json" {
		t.Errorf("unexpected entries: %+v", entries)
	}
}

// TestDoctorJSONExitsNonZeroOnError asserts --json composes with the
// error-exit policy: JSON still lands on stdout, error still surfaces
// for the shell to map to ExitState.
func TestDoctorJSONExitsNonZeroOnError(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	// Create a dangling symlink to trigger a doctor error.
	runtimeLink := filepath.Join(root, ".claude-work/settings.json")
	target := filepath.Join(root, ".config/ccp/profiles/work/settings.json")
	if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, runtimeLink); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(target) // make the link dangle

	out, _, err := runCLI(t, "", "profile", "doctor", "--json")
	if err == nil {
		t.Fatal("expected error from doctor on dangling symlink")
	}
	// JSON should still be on stdout even though the command errored.
	var findings []profile.DoctorFinding
	if jerr := json.Unmarshal([]byte(out), &findings); jerr != nil {
		t.Fatalf("unmarshal: %v\nout: %s", jerr, out)
	}
	if len(findings) == 0 || findings[0].Severity != "error" {
		t.Errorf("unexpected findings: %+v", findings)
	}
}

// TestCurrentJSONDistinguishesNullFromEmpty asserts `ccp current --json`
// emits a structured marker for "no active profile" that an agent can
// unambiguously parse.
func TestCurrentJSONDistinguishesNullFromEmpty(t *testing.T) {
	setupCLI(t)

	// No active yet.
	out, _, err := runCLI(t, "", "current", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"active": null`) {
		t.Errorf("empty: expected null, got %s", out)
	}

	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "use", "work"); err != nil {
		t.Fatal(err)
	}
	out, _, err = runCLI(t, "", "current", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"active": "work"`) {
		t.Errorf("active: expected work, got %s", out)
	}
}

// TestVersionJSONShape asserts `ccp version --json` produces {"version": ...}.
func TestVersionJSONShape(t *testing.T) {
	setupCLI(t)
	out, _, err := runCLI(t, "", "version", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ Version string }
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("unmarshal: %v\nout: %s", err, out)
	}
	if v.Version == "" {
		t.Errorf("empty version in %s", out)
	}
}
