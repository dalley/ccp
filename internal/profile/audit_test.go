package profile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plantFile is a tiny helper that writes a file under the profile's
// source tree with a readable mode and bails the test on failure.
func plantFile(t *testing.T, pr Profile, rel string, content string) {
	t.Helper()
	full := filepath.Join(pr.SourceDir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAuditNoSecretsReturnsNoFindings(t *testing.T) {
	p := setupHome(t)
	pr, err := Create(p, "work", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plantFile(t, pr, "settings.json", `{"model": "sonnet"}`)
	plantFile(t, pr, "README.md", "Plain prose, no secrets here.")

	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("want 0 findings, got %d: %+v", len(findings), findings)
	}
}

func TestAuditDetectsAWSKey(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	plantFile(t, pr, "settings.json", `{"aws": "AKIAIOSFODNN7EXAMPLE"}`)

	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Kind != "aws" {
		t.Fatalf("want 1 aws finding, got %+v", findings)
	}
	// Preview must not leak the full secret.
	if strings.Contains(findings[0].Preview, "IOSFODNN7") {
		t.Errorf("preview leaks middle of secret: %q", findings[0].Preview)
	}
}

func TestAuditDetectsGitHubClassicPAT(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	// ghp_ + exactly 36 chars of [A-Za-z0-9].
	token := "ghp_" + strings.Repeat("A", 36)
	plantFile(t, pr, "settings.json", `token = "`+token+`"`)

	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Kind != "github" {
		t.Fatalf("want 1 github finding, got %+v", findings)
	}
}

func TestAuditDetectsGitHubFineGrainedPAT(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	// github_pat_ + 22 + _ + 59
	token := "github_pat_" + strings.Repeat("A", 22) + "_" + strings.Repeat("b", 59)
	plantFile(t, pr, "settings.json", `token = "`+token+`"`)

	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Kind != "github" {
		t.Fatalf("want 1 github finding, got %+v", findings)
	}
}

func TestAuditDetectsStripeLiveKey(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	plantFile(t, pr, "settings.json", `stripe = "sk_live_abcdef0123456789ABCDEF"`)

	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Kind != "stripe" {
		t.Fatalf("want 1 stripe finding, got %+v", findings)
	}
}

func TestAuditDetectsPEMBlock(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	plantFile(t, pr, "id_rsa", `-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEAwN...
-----END RSA PRIVATE KEY-----
`)
	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	foundPEM := false
	for _, f := range findings {
		if f.Kind == "pem" {
			foundPEM = true
		}
	}
	if !foundPEM {
		t.Errorf("want pem finding, got %+v", findings)
	}
}

func TestAuditDetectsEntropyBase64(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	// 40 chars of high-entropy base64.
	plantFile(t, pr, "settings.json", `key = "aB3dE5fG7hI9jK1lM2nO4pQ6rS8tU0vWxYzQw123"`)

	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 || findings[0].Kind != "entropy" {
		t.Fatalf("want entropy finding, got %+v", findings)
	}
}

func TestAuditDetectsJWT(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	// A realistic-looking JWT (3 dot-separated base64url segments).
	plantFile(t, pr, "settings.json",
		`jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"`)

	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	foundJWT := false
	for _, f := range findings {
		if f.Kind == "jwt" {
			foundJWT = true
		}
	}
	if !foundJWT {
		t.Errorf("want jwt finding, got %+v", findings)
	}
}

func TestAuditSkipsKeychainRef(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	plantFile(t, pr, "settings.json", `anthropic = "{{ keychain:ANTHROPIC_API_KEY }}"`)

	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("ref-bearing line should not be flagged: %+v", findings)
	}
}

func TestAuditSkipsEscapedRef(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	plantFile(t, pr, "README.md", `To reference a key, use {{!}}{{ keychain:API_KEY }} syntax.`)

	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("escaped ref line should not be flagged: %+v", findings)
	}
}

func TestAuditRespectsIgnoreMarker(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	plantFile(t, pr, "example.md",
		`Example: ghp_0123456789ABCDEFabcdef0123456789ABCDEF  # ccp:audit-ignore`)

	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("ignore marker should suppress finding: %+v", findings)
	}
}

func TestAuditDoesNotFlagUUID(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	// UUIDs are 36 chars, ~3.2 entropy — should NOT trip the 4.0
	// threshold. Include a few on different lines.
	plantFile(t, pr, "settings.json", `
id1 = "550e8400-e29b-41d4-a716-446655440000"
id2 = "123e4567-e89b-12d3-a456-426614174000"
`)
	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Kind == "entropy" {
			t.Errorf("UUID flagged as entropy: %+v", f)
		}
	}
}

func TestAuditFlagsGitSHA256Hash(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	// 64-char hex — genuine hash, high entropy, hex charset passes.
	plantFile(t, pr, "settings.json",
		`sha = "3a7bd3e2360a3d29eea436fcfb7e44c735d117c42d1c1835420b6b9942dd4f1b"`)

	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	foundEntropy := false
	for _, f := range findings {
		if f.Kind == "entropy" {
			foundEntropy = true
		}
	}
	if !foundEntropy {
		t.Errorf("64-char sha256 hex should be flagged: %+v", findings)
	}
}

func TestAuditSkipsLargeFile(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	// Write 1.5 MiB of printable content — avoids the binary-skip
	// path so we exercise the size-limit branch specifically.
	big := strings.Repeat("abcdefghij", (1536*1024)/10)
	plantFile(t, pr, "big.txt", big)

	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	foundSkip := false
	for _, f := range findings {
		if f.Kind == "skipped-large" {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Errorf("want skipped-large finding, got %+v", findings)
	}
}

func TestAuditSkipsBinaryFile(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	// Plant a file whose first 512 bytes include a NUL.
	buf := make([]byte, 600)
	for i := range buf {
		buf[i] = byte('x')
	}
	buf[42] = 0
	if err := os.WriteFile(filepath.Join(pr.SourceDir, "blob.bin"), buf, 0o600); err != nil {
		t.Fatal(err)
	}
	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	foundSkip := false
	for _, f := range findings {
		if f.Kind == "skipped-binary" {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Errorf("want skipped-binary finding, got %+v", findings)
	}
}

func TestAuditMissingProfileReturnsErrNotFound(t *testing.T) {
	p := setupHome(t)
	_, err := Audit(p, "ghost")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestAuditInvalidProfileNameRejected(t *testing.T) {
	p := setupHome(t)
	_, err := Audit(p, "BadName!")
	if err == nil {
		t.Error("want validation error for bad name")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("bad name should be a validation error, not ErrNotFound")
	}
}

func TestAuditFindingsSortedStably(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	// Plant matches in three files so the sort has work to do.
	plantFile(t, pr, "z.md", `AKIAIOSFODNN7EXAMPLE`)
	plantFile(t, pr, "a.md", `AKIAIOSFODNN7EXAMPLE`)
	plantFile(t, pr, "m.md", "line1\nAKIAIOSFODNN7EXAMPLE\nAKIAIOSFODNN7EXAMPLE\n")

	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	// Expect order: a.md:1, m.md:2, m.md:3, z.md:1
	if len(findings) != 4 {
		t.Fatalf("want 4 findings, got %d: %+v", len(findings), findings)
	}
	if findings[0].File != "a.md" || findings[1].File != "m.md" ||
		findings[2].File != "m.md" || findings[3].File != "z.md" {
		t.Errorf("unstable order: %+v", findings)
	}
	if findings[1].Line != 2 || findings[2].Line != 3 {
		t.Errorf("line order wrong: %+v", findings)
	}
}

func TestAuditPreviewNeverLeaksMoreThan8Chars(t *testing.T) {
	p := setupHome(t)
	pr, _ := Create(p, "work", CreateOptions{})
	// Plant a fake high-entropy secret with a distinctive middle that
	// must NOT appear in any preview.
	middle := "THIS_IS_MIDDLE_OF_SECRET"
	secret := "pre1" + middle + "post"
	plantFile(t, pr, "settings.json", `s = "`+secret+`"`)

	findings, err := Audit(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if strings.Contains(f.Preview, middle) {
			t.Errorf("preview leaks middle: %q", f.Preview)
		}
		// Count runes so multi-byte ellipsis doesn't inflate length.
		runes := []rune(f.Preview)
		// Skip info-level findings whose Preview is a human message.
		if f.Kind == "skipped-large" || f.Kind == "skipped-binary" {
			continue
		}
		// Preview = 4 + 1 (ellipsis rune) + 4 = 9 runes max.
		if len(runes) > 9 {
			t.Errorf("preview too long: %q (%d runes)", f.Preview, len(runes))
		}
	}
}

func TestShannonEntropyBasicProperties(t *testing.T) {
	// All-same-byte string: entropy = 0.
	if h := shannonEntropy(strings.Repeat("a", 40)); h != 0 {
		t.Errorf("entropy of repeated byte = %v, want 0", h)
	}
	// Uniform over 64 symbols (base64 alphabet): entropy approaches 6.
	// We just check it's above 4 for a reasonable sample.
	sample := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	if h := shannonEntropy(sample); h < 5.5 {
		t.Errorf("entropy of full base64 alphabet = %v, want >= 5.5", h)
	}
}

func TestRedactShape(t *testing.T) {
	got := redact("AKIAIOSFODNN7EXAMPLE")
	if got != "AKIA…MPLE" {
		t.Errorf("redact = %q, want %q", got, "AKIA…MPLE")
	}
}
