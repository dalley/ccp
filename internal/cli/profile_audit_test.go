package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileAuditCleanExitZero(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/settings.json"),
		[]byte(`{"model":"sonnet"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runCLI(t, "", "profile", "audit", "work")
	if err != nil {
		t.Fatalf("audit: %v\nstdout:%s\nstderr:%s", err, stdout, stderr)
	}
	// Clean profile keeps stdout silent so the command is
	// pipeline-friendly; humans get the message on stderr.
	if stdout != "" {
		t.Errorf("stdout should be empty, got: %q", stdout)
	}
	if !strings.Contains(stderr, "no suspected secrets") {
		t.Errorf("stderr missing clean message: %q", stderr)
	}
}

func TestProfileAuditDetectsAWSKeyExit4(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/settings.json"),
		[]byte(`aws = "AKIAIOSFODNN7EXAMPLE"`), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runCLI(t, "", "profile", "audit", "work")
	if err == nil {
		t.Fatal("expected ErrAuditSecretsDetected")
	}
	if ExitCodeFor(err) != ExitConflict {
		t.Errorf("exit code = %d, want %d", ExitCodeFor(err), ExitConflict)
	}
	if !strings.Contains(stdout, "[aws]") || !strings.Contains(stdout, "settings.json") {
		t.Errorf("stdout missing finding: %q", stdout)
	}
	// Preview should leak at most 8 source chars — verify the
	// middle of the canonical test key is NOT present.
	if strings.Contains(stdout, "IOSFODNN7") {
		t.Errorf("preview leaks middle of secret: %q", stdout)
	}
}

func TestProfileAuditJSONValidOnNonzeroExit(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/settings.json"),
		[]byte(`aws = "AKIAIOSFODNN7EXAMPLE"`), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runCLI(t, "", "profile", "audit", "work", "--json")
	if err == nil {
		t.Fatal("expected ErrAuditSecretsDetected")
	}
	if ExitCodeFor(err) != ExitConflict {
		t.Errorf("exit code = %d, want %d", ExitCodeFor(err), ExitConflict)
	}
	// JSON must parse even though we exited nonzero.
	var payload struct {
		Profile  string                   `json:"profile"`
		Detected int                      `json:"detected"`
		Findings []map[string]interface{} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("JSON parse: %v\nraw: %s", err, stdout)
	}
	if payload.Profile != "work" {
		t.Errorf("profile = %q, want work", payload.Profile)
	}
	if payload.Detected != 1 || len(payload.Findings) != 1 {
		t.Errorf("detected=%d len=%d, want 1/1", payload.Detected, len(payload.Findings))
	}
	if payload.Findings[0]["kind"] != "aws" {
		t.Errorf("kind = %v, want aws", payload.Findings[0]["kind"])
	}
}

func TestProfileAuditJSONCleanEmitsEmptyFindingsArray(t *testing.T) {
	setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runCLI(t, "", "profile", "audit", "work", "--json")
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	var payload struct {
		Profile  string                   `json:"profile"`
		Detected int                      `json:"detected"`
		Findings []map[string]interface{} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("JSON parse: %v\nraw: %s", err, stdout)
	}
	if payload.Findings == nil {
		t.Error("findings should be [] not null")
	}
	if payload.Detected != 0 {
		t.Errorf("detected = %d, want 0", payload.Detected)
	}
}

func TestProfileAuditJSONFindingsStableOrder(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	// Three files — the sort must produce a.md, m.md, z.md.
	for _, name := range []string{"z.md", "a.md", "m.md"} {
		if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work", name),
			[]byte("AKIAIOSFODNN7EXAMPLE"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stdout, _, _ := runCLI(t, "", "profile", "audit", "work", "--json")
	var payload struct {
		Findings []struct {
			File string `json:"file"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("JSON: %v\n%s", err, stdout)
	}
	if len(payload.Findings) != 3 {
		t.Fatalf("want 3, got %d: %+v", len(payload.Findings), payload.Findings)
	}
	if payload.Findings[0].File != "a.md" ||
		payload.Findings[1].File != "m.md" ||
		payload.Findings[2].File != "z.md" {
		t.Errorf("order: %+v", payload.Findings)
	}
}

func TestProfileAuditMissingProfileExitUser(t *testing.T) {
	setupCLI(t)
	_, _, err := runCLI(t, "", "profile", "audit", "ghost")
	if err == nil {
		t.Fatal("expected ErrNotFound")
	}
	if ExitCodeFor(err) != ExitUser {
		t.Errorf("exit code = %d, want %d", ExitCodeFor(err), ExitUser)
	}
}

func TestProfileAuditKeychainRefNotFlagged(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/settings.json"),
		[]byte(`anthropic = "{{ keychain:ANTHROPIC_API_KEY }}"`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCLI(t, "", "profile", "audit", "work")
	if err != nil {
		t.Errorf("ref-bearing line shouldn't trip audit: %v", err)
	}
}

func TestProfileAuditEndToEnd_BinarySkipDoesNotFail(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	// Plant a binary blob. The audit must not fail — a binary file
	// is informational, not a conflict.
	buf := make([]byte, 1024)
	buf[0] = 0 // NUL in first 512 bytes triggers the binary-skip path
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/blob.bin"),
		buf, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCLI(t, "", "profile", "audit", "work")
	if err != nil {
		t.Errorf("binary file should not cause audit failure: %v", err)
	}
}
