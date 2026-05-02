package profile

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dalley/ccp/internal/refs"
)

// extractTar reads a tar stream into a map[relpath] -> bytes. Symlinks
// and directories are recorded with sentinel values so tests can assert
// on their presence.
func extractTar(t *testing.T, buf []byte) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	tr := tar.NewReader(bytes.NewReader(buf))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			out["DIR:"+strings.TrimSuffix(hdr.Name, "/")] = nil
		case tar.TypeSymlink:
			out["LINK:"+hdr.Name] = []byte(hdr.Linkname)
		case tar.TypeReg:
			b, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read %s: %v", hdr.Name, err)
			}
			out[hdr.Name] = b
		}
	}
	return out
}

// tarHeaderMode returns the mode bits of the named regular-file entry.
// Fails the test if the entry is absent.
func tarHeaderMode(t *testing.T, buf []byte, name string) os.FileMode {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(buf))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			t.Fatalf("tar entry %q not found", name)
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		if hdr.Name == name {
			return os.FileMode(hdr.Mode).Perm()
		}
	}
}

func TestExportDefaultStripsSecretsAndPreservesRefs(t *testing.T) {
	p := setupHome(t)
	pr, err := Create(p, "work", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plantFile(t, pr, "settings.json", `{"anthropic":"{{ keychain:ANTHROPIC_API_KEY }}"}`)
	plantFile(t, pr, "hooks/before.sh", "#!/bin/sh\necho hi\n")
	// Plant a secrets/<name>.json on disk to verify it is NOT shipped.
	if err := os.MkdirAll(p.SecretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.SecretFilePath("work"),
		[]byte(`{"ANTHROPIC_API_KEY":"sk-should-not-appear"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	err = Export(p, "work", ExportOptions{Hostname: "testhost", Now: time.Date(2026, 4, 23, 15, 0, 0, 0, time.UTC)}, &buf)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	files := extractTar(t, buf.Bytes())

	// Manifest present and shaped correctly.
	raw, ok := files[ExportManifestName]
	if !ok {
		t.Fatalf("manifest missing: keys=%v", keys(files))
	}
	var m exportManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest JSON: %v\nraw: %s", err, raw)
	}
	if m.SchemaVersion != 1 || m.Profile != "work" {
		t.Errorf("manifest shape: %+v", m)
	}
	if m.ContainsSecrets {
		t.Errorf("default must NOT claim secrets, got: %+v", m)
	}
	if len(m.InlinedFiles) != 0 {
		t.Errorf("default must not inline: %+v", m.InlinedFiles)
	}
	if m.ExporterHostname != "testhost" {
		t.Errorf("hostname = %q, want testhost", m.ExporterHostname)
	}

	// settings.json shipped VERBATIM — ref syntax preserved.
	if got := string(files["settings.json"]); !strings.Contains(got, "{{ keychain:ANTHROPIC_API_KEY }}") {
		t.Errorf("settings.json ref not preserved: %q", got)
	}
	if strings.Contains(string(files["settings.json"]), "sk-should-not-appear") {
		t.Errorf("cleartext secret leaked into default export: %q", files["settings.json"])
	}

	// secrets/<name>.json MUST NOT be in the tar.
	for k := range files {
		if strings.HasPrefix(k, "secrets/") {
			t.Errorf("secrets entry leaked into default export: %q", k)
		}
	}
}

func TestExportIncludeSecretsInlinesAndShipsFile(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	plantFile(t, pr, "settings.json", `{"anthropic":"{{ keychain:ANTHROPIC_API_KEY }}"}`)
	plantFile(t, pr, "README.md", "no refs here\n")
	if err := os.MkdirAll(p.SecretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	secretBody := []byte(`{"OTHER_KEY":"xyz"}`)
	if err := os.WriteFile(p.SecretFilePath("work"), secretBody, 0o600); err != nil {
		t.Fatal(err)
	}

	resolver := fakeKeyring{"ANTHROPIC_API_KEY": "sk-test-resolved"}
	var buf bytes.Buffer
	err := Export(p, "work", ExportOptions{
		IncludeSecrets: true,
		SkipAudit:      true,
		Hostname:       "testhost",
		Now:            time.Date(2026, 4, 23, 15, 0, 0, 0, time.UTC),
		Resolver: refs.DefaultResolver{
			Profile: "work",
			KeyringGet: func(_, _, key string) (string, error) {
				v, ok := resolver[key]
				if !ok {
					return "", errNotFound
				}
				return v, nil
			},
		},
	}, &buf)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	files := extractTar(t, buf.Bytes())

	var m exportManifest
	if err := json.Unmarshal(files[ExportManifestName], &m); err != nil {
		t.Fatal(err)
	}
	if !m.ContainsSecrets {
		t.Errorf("ContainsSecrets must be true: %+v", m)
	}
	if len(m.InlinedFiles) != 1 || m.InlinedFiles[0] != "settings.json" {
		t.Errorf("InlinedFiles = %v, want [settings.json]", m.InlinedFiles)
	}

	// settings.json bytes are resolved.
	if !strings.Contains(string(files["settings.json"]), "sk-test-resolved") {
		t.Errorf("settings.json not resolved: %q", files["settings.json"])
	}
	// README.md (no refs) passes through unchanged.
	if string(files["README.md"]) != "no refs here\n" {
		t.Errorf("README mangled: %q", files["README.md"])
	}
	// secrets file shipped byte-for-byte.
	if got := files["secrets/work.json"]; !bytes.Equal(got, secretBody) {
		t.Errorf("secrets file mismatch: %q vs %q", got, secretBody)
	}
}

func TestExportFailOnAuditRefusesWhenAWSKeyPresent(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	plantFile(t, pr, "settings.json", `aws = "AKIAIOSFODNN7EXAMPLE"`)

	var buf bytes.Buffer
	err := Export(p, "work", ExportOptions{FailOnAudit: true}, &buf)
	if err == nil {
		t.Fatal("expected ErrAuditSecretsDetected")
	}
	if !errors.Is(err, ErrAuditSecretsDetected) {
		t.Errorf("error chain missing ErrAuditSecretsDetected: %v", err)
	}
}

func TestExportFailOnAuditCleanProfileSucceeds(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	plantFile(t, pr, "README.md", "plain prose, no secrets\n")

	var buf bytes.Buffer
	if err := Export(p, "work", ExportOptions{FailOnAudit: true}, &buf); err != nil {
		t.Fatalf("clean profile should pass --fail-on-audit: %v", err)
	}
	if len(buf.Bytes()) == 0 {
		t.Fatal("tar empty")
	}
}

func TestExportSkipAuditDoesNotScan(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	// Plant an AWS key — would trigger the audit gate if scanned.
	plantFile(t, pr, "settings.json", `aws = "AKIAIOSFODNN7EXAMPLE"`)

	var buf bytes.Buffer
	var advisory []AuditFinding
	err := Export(p, "work", ExportOptions{
		FailOnAudit:   true,
		SkipAudit:     true,
		AuditAdvisory: &advisory,
	}, &buf)
	if err != nil {
		t.Fatalf("skip-audit must win over fail-on-audit: %v", err)
	}
	if len(advisory) != 0 {
		t.Errorf("advisory populated despite skip-audit: %+v", advisory)
	}
}

func TestExportMissingProfileReturnsErrNotFound(t *testing.T) {
	p := setupHome(t)
	var buf bytes.Buffer
	err := Export(p, "ghost", ExportOptions{}, &buf)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestExportInvalidProfileName(t *testing.T) {
	p := setupHome(t)
	var buf bytes.Buffer
	err := Export(p, "NotValid!", ExportOptions{}, &buf)
	if err == nil {
		t.Fatal("expected name validation to fail")
	}
}

func TestExportManifestListsEveryFile(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	plantFile(t, pr, "a.txt", "a\n")
	plantFile(t, pr, "b/c.txt", "c\n")
	plantFile(t, pr, "b/d/e.txt", "e\n")

	var buf bytes.Buffer
	if err := Export(p, "work", ExportOptions{SkipAudit: true}, &buf); err != nil {
		t.Fatal(err)
	}
	files := extractTar(t, buf.Bytes())
	var m exportManifest
	if err := json.Unmarshal(files[ExportManifestName], &m); err != nil {
		t.Fatal(err)
	}
	want := []string{"a.txt", "b", "b/c.txt", "b/d", "b/d/e.txt"}
	got := append([]string(nil), m.Files...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Files list\ngot  %v\nwant %v", got, want)
	}
}

func TestExportPreservesFilePerms(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	// Plant an executable hook — ensure the x bit rides along.
	full := filepath.Join(pr.SourceDir, "hooks", "run.sh")
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Export(p, "work", ExportOptions{SkipAudit: true}, &buf); err != nil {
		t.Fatal(err)
	}
	mode := tarHeaderMode(t, buf.Bytes(), "hooks/run.sh")
	if mode&0o111 == 0 {
		t.Errorf("exec bit missing from hooks/run.sh mode=%o", mode)
	}
}

func TestExportAuditAdvisoryLeaksFindings(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	plantFile(t, pr, "settings.json", `aws = "AKIAIOSFODNN7EXAMPLE"`)

	var advisory []AuditFinding
	var buf bytes.Buffer
	err := Export(p, "work", ExportOptions{AuditAdvisory: &advisory}, &buf)
	if err != nil {
		t.Fatalf("advisory-mode should not block: %v", err)
	}
	if len(advisory) != 1 || advisory[0].Kind != "aws" {
		t.Errorf("advisory = %+v", advisory)
	}
	// Tar still produced despite findings.
	if len(buf.Bytes()) == 0 {
		t.Error("tar empty despite advisory-only audit")
	}
}

func TestExportIncludeSecretsUnresolvedKeychainReturnsTypedErr(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	plantFile(t, pr, "settings.json", `{"x":"{{ keychain:MISSING_KEY }}"}`)

	var buf bytes.Buffer
	err := Export(p, "work", ExportOptions{
		IncludeSecrets: true,
		SkipAudit:      true,
		Resolver: refs.DefaultResolver{
			Profile: "work",
			KeyringGet: func(_, _, key string) (string, error) {
				return "", errNotFound
			},
		},
	}, &buf)
	if err == nil {
		t.Fatal("expected unresolved-ref error")
	}
	if !errors.Is(err, refs.ErrSecretRefUnresolved) {
		t.Errorf("expected ErrSecretRefUnresolved, got %v", err)
	}
	if !strings.Contains(err.Error(), "MISSING_KEY") {
		t.Errorf("error should name the ref: %v", err)
	}
}

func TestExportRoundTripByteIdentical(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	plantFile(t, pr, "settings.json", `{"model":"sonnet"}`)
	plantFile(t, pr, "hooks/pre.sh", "echo hi\n")

	var buf bytes.Buffer
	if err := Export(p, "work", ExportOptions{SkipAudit: true}, &buf); err != nil {
		t.Fatal(err)
	}
	files := extractTar(t, buf.Bytes())
	if string(files["settings.json"]) != `{"model":"sonnet"}` {
		t.Errorf("settings mismatch: %q", files["settings.json"])
	}
	if string(files["hooks/pre.sh"]) != "echo hi\n" {
		t.Errorf("hook mismatch: %q", files["hooks/pre.sh"])
	}
}

func TestExportSymlinkInTreeIsShipped(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	plantFile(t, pr, "base.sh", "echo base\n")
	// In-tree relative symlink: alias.sh -> base.sh.
	alias := filepath.Join(pr.SourceDir, "alias.sh")
	if err := os.Symlink("base.sh", alias); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	var buf bytes.Buffer
	if err := Export(p, "work", ExportOptions{SkipAudit: true}, &buf); err != nil {
		t.Fatal(err)
	}
	files := extractTar(t, buf.Bytes())
	if got, ok := files["LINK:alias.sh"]; !ok || string(got) != "base.sh" {
		t.Errorf("symlink missing or wrong: %v (ok=%v)", string(got), ok)
	}
}

func TestExportSymlinkEscapingIsRefused(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	outside := filepath.Join(t.TempDir(), "evil.txt")
	if err := os.WriteFile(outside, []byte("hacker"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Symlink pointing out of the source tree.
	link := filepath.Join(pr.SourceDir, "pwned")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	var buf bytes.Buffer
	err := Export(p, "work", ExportOptions{SkipAudit: true}, &buf)
	if err == nil {
		t.Fatal("expected refusal of escaping symlink")
	}
	if !strings.Contains(err.Error(), "escapes profile source") {
		t.Errorf("error should name the escape: %v", err)
	}
}

// TestExportHostnameDefault verifies the zero-override path calls
// os.Hostname — we only check the field is non-empty (the actual host
// name varies across test environments).
func TestExportHostnameDefault(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	plantFile(t, pr, "x.txt", "x\n")

	var buf bytes.Buffer
	if err := Export(p, "work", ExportOptions{SkipAudit: true}, &buf); err != nil {
		t.Fatal(err)
	}
	files := extractTar(t, buf.Bytes())
	var m exportManifest
	if err := json.Unmarshal(files[ExportManifestName], &m); err != nil {
		t.Fatal(err)
	}
	if m.ExporterHostname == "" {
		t.Error("hostname must default to os.Hostname")
	}
}

// TestExportEmptyProfileIsUsable verifies the degenerate case: profile
// with no files still produces a valid tar with just the manifest.
func TestExportEmptyProfile(t *testing.T) {
	p := setupHome(t)
	if _, err := Create(p, "empty", CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Export(p, "empty", ExportOptions{SkipAudit: true}, &buf); err != nil {
		t.Fatal(err)
	}
	files := extractTar(t, buf.Bytes())
	if _, ok := files[ExportManifestName]; !ok {
		t.Error("empty profile should still produce a manifest")
	}
}

// --- helpers ------------------------------------------------------------

// fakeKeyring is a tiny map-backed keychain for resolver tests.
type fakeKeyring map[string]string

var errNotFound = errors.New("not found")

// keys returns the sorted key list of the map — used only to format
// debug output when a tar assertion fails.
func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
