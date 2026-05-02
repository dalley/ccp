package cli

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dalley/ccp/internal/profile"
	"github.com/dalley/ccp/internal/refs"
)

// cliExtractTar reads a tar byte stream into a flat map — mirrors the
// helper in internal/profile/export_test.go so the CLI tests stay
// self-contained (the two packages don't share a test helpers file).
func cliExtractTar(t *testing.T, buf []byte) map[string][]byte {
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
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = b
	}
	return out
}

func TestProfileExportDefaultWritesTarWithRefsPreserved(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/settings.json"),
		[]byte(`{"x":"{{ keychain:K }}"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runCLI(t, "", "profile", "export", "work")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	files := cliExtractTar(t, []byte(stdout))
	if _, ok := files[profile.ExportManifestName]; !ok {
		t.Fatalf("manifest missing")
	}
	if got := string(files["settings.json"]); !strings.Contains(got, "{{ keychain:K }}") {
		t.Errorf("ref syntax mangled: %q", got)
	}
	for k := range files {
		if strings.HasPrefix(k, "secrets/") {
			t.Errorf("secrets entry leaked in default: %q", k)
		}
	}
	// Manifest should say contains_secrets=false.
	var m struct {
		ContainsSecrets bool `json:"contains_secrets"`
	}
	if err := json.Unmarshal(files[profile.ExportManifestName], &m); err != nil {
		t.Fatal(err)
	}
	if m.ContainsSecrets {
		t.Error("contains_secrets must be false by default")
	}
}

func TestProfileExportToFileIsZeroSixHundred(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/settings.json"),
		[]byte(`{"m":"s"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(t.TempDir(), "work.tar")
	if _, _, err := runCLI(t, "", "profile", "export", "work", "-o", outPath); err != nil {
		t.Fatalf("export: %v", err)
	}
	st, err := os.Stat(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0o600", st.Mode().Perm())
	}
	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cliExtractTar(t, b)[profile.ExportManifestName]; !ok {
		t.Error("written tar is missing manifest")
	}
}

func TestProfileExportRefusesStdoutTTY(t *testing.T) {
	setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}

	// Force the TTY probe to return true. Tests normally capture
	// stdout via a bytes.Buffer, where the probe returns false —
	// stubbing here verifies the refusal path.
	orig := exportStdoutIsTTY
	exportStdoutIsTTY = func(*os.File) bool { return true }
	t.Cleanup(func() { exportStdoutIsTTY = orig })
	// But cobra's test writer isn't os.Stdout, so the helper
	// returns nil from osStdoutForExport. To actually exercise the
	// TTY path, also stub osStdoutForExport indirectly: set stdin to
	// be a nil probe so the code sees "TTY = true" on the stdout
	// check. The simplest thing is to redirect the tree: make the
	// stub return true even for nil.
	// (The existing stub above already returns true for nil, so the
	// refusal fires regardless of the cobra writer type.)

	_, _, err := runCLI(t, "", "profile", "export", "work")
	if err == nil {
		t.Fatal("expected refusal on TTY stdout")
	}
	if !strings.Contains(err.Error(), "terminal") {
		t.Errorf("error should mention terminal: %v", err)
	}
}

func TestProfileExportIncludeSecretsRequiresYesReallyNonTTY(t *testing.T) {
	setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}

	// Stdin in tests is empty/non-TTY — stub returns false by default.
	orig := exportStdinIsTTY
	exportStdinIsTTY = func(*os.File) bool { return false }
	t.Cleanup(func() { exportStdinIsTTY = orig })

	_, _, err := runCLI(t, "", "profile", "export", "work", "--include-secrets")
	if err == nil {
		t.Fatal("expected refusal without --yes-really in non-TTY")
	}
	if !strings.Contains(err.Error(), "--yes-really") {
		t.Errorf("error should point user at --yes-really: %v", err)
	}
}

func TestProfileExportIncludeSecretsWithYesReallyBypassesTTYGuard(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	// File has no refs — skip-audit avoids noise.
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/plain.txt"),
		[]byte("no refs\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runCLI(t, "", "profile", "export", "work",
		"--include-secrets", "--yes-really", "--skip-audit")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	files := cliExtractTar(t, []byte(stdout))
	var m struct {
		ContainsSecrets bool `json:"contains_secrets"`
	}
	if err := json.Unmarshal(files[profile.ExportManifestName], &m); err != nil {
		t.Fatal(err)
	}
	if !m.ContainsSecrets {
		t.Error("contains_secrets must be true with --include-secrets")
	}
}

func TestProfileExportIncludeSecretsTTYDeclineExitsUser(t *testing.T) {
	setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	origIn := exportStdinIsTTY
	origOut := exportStdoutIsTTY
	exportStdinIsTTY = func(*os.File) bool { return true }
	exportStdoutIsTTY = func(*os.File) bool { return false }
	t.Cleanup(func() {
		exportStdinIsTTY = origIn
		exportStdoutIsTTY = origOut
	})

	// User types "n\n".
	_, _, err := runCLI(t, "n\n", "profile", "export", "work", "--include-secrets")
	if err == nil {
		t.Fatal("expected decline to produce an error")
	}
	if ExitCodeFor(err) != ExitUser {
		t.Errorf("decline should map to ExitUser, got %d (%v)", ExitCodeFor(err), err)
	}
}

func TestProfileExportIncludeSecretsTTYAcceptProceeds(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/plain.txt"),
		[]byte("no refs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	origIn := exportStdinIsTTY
	exportStdinIsTTY = func(*os.File) bool { return true }
	t.Cleanup(func() { exportStdinIsTTY = origIn })

	stdout, _, err := runCLI(t, "y\n", "profile", "export", "work",
		"--include-secrets", "--skip-audit")
	if err != nil {
		t.Fatalf("export after accept: %v", err)
	}
	if !strings.Contains(stdout, "EXPORT_MANIFEST.json") {
		// The prompt goes to stdout; but the tar does too, in the
		// same buffer. Confirm the tar was produced by scanning for
		// the manifest filename string (which the tar header carries).
		t.Errorf("no tar header detected in stdout (len=%d)", len(stdout))
	}
}

func TestProfileExportFailOnAuditAWSKeyExitsConflict(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/settings.json"),
		[]byte(`aws = "AKIAIOSFODNN7EXAMPLE"`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := runCLI(t, "", "profile", "export", "work", "--fail-on-audit")
	if err == nil {
		t.Fatal("expected fail-on-audit to reject")
	}
	if !errors.Is(err, profile.ErrAuditSecretsDetected) {
		t.Errorf("expected ErrAuditSecretsDetected, got %v", err)
	}
	if ExitCodeFor(err) != ExitConflict {
		t.Errorf("exit = %d, want %d", ExitCodeFor(err), ExitConflict)
	}
}

func TestProfileExportAdvisoryAuditPrintsStderrProceedsStdout(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/settings.json"),
		[]byte(`aws = "AKIAIOSFODNN7EXAMPLE"`), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCLI(t, "", "profile", "export", "work")
	if err != nil {
		t.Fatalf("advisory mode must not block: %v", err)
	}
	if !strings.Contains(stderr, "suspected secret") {
		t.Errorf("advisory hint missing from stderr: %q", stderr)
	}
	// Tar must land on stdout; stderr-only advisory doesn't pollute it.
	if _, ok := cliExtractTar(t, []byte(stdout))[profile.ExportManifestName]; !ok {
		t.Error("stdout should still carry a valid tar")
	}
}

func TestProfileExportSkipAuditSilent(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/settings.json"),
		[]byte(`aws = "AKIAIOSFODNN7EXAMPLE"`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := runCLI(t, "", "profile", "export", "work", "--skip-audit")
	if err != nil {
		t.Fatalf("skip-audit export: %v", err)
	}
	if strings.Contains(stderr, "suspected secret") {
		t.Errorf("skip-audit should NOT print advisory: %q", stderr)
	}
}

func TestProfileExportMissingProfileExitsUser(t *testing.T) {
	setupCLI(t)
	_, _, err := runCLI(t, "", "profile", "export", "ghost")
	if err == nil {
		t.Fatal("expected missing-profile error")
	}
	if ExitCodeFor(err) != ExitUser {
		t.Errorf("exit = %d, want %d", ExitCodeFor(err), ExitUser)
	}
}

func TestProfileExportIncludeSecretsUnresolvedRefExitsState(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/settings.json"),
		[]byte(`{"x":"{{ env.THIS_VAR_IS_NOT_SET_ANYWHERE_XYZ }}"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Use env ref (not keychain) to keep the test hermetic —
	// keychain refs would pull in go-keyring / MockInit concerns.
	t.Setenv("THIS_VAR_IS_NOT_SET_ANYWHERE_XYZ", "")
	os.Unsetenv("THIS_VAR_IS_NOT_SET_ANYWHERE_XYZ")

	_, _, err := runCLI(t, "", "profile", "export", "work",
		"--include-secrets", "--yes-really", "--skip-audit")
	if err == nil {
		t.Fatal("expected unresolved env ref to error")
	}
	if !errors.Is(err, refs.ErrSecretRefUnresolved) {
		t.Errorf("expected ErrSecretRefUnresolved, got %v", err)
	}
	if ExitCodeFor(err) != ExitState {
		t.Errorf("exit = %d, want %d", ExitCodeFor(err), ExitState)
	}
	if !strings.Contains(err.Error(), "THIS_VAR_IS_NOT_SET_ANYWHERE_XYZ") {
		t.Errorf("error should name the ref: %v", err)
	}
}

func TestProfileExportTarRoundtripExtraction(t *testing.T) {
	root := setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/a.txt"),
		[]byte("A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".config/ccp/profiles/work/sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config/ccp/profiles/work/sub/b.txt"),
		[]byte("B\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(t.TempDir(), "x.tar")
	if _, _, err := runCLI(t, "", "profile", "export", "work", "-o", outPath); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	files := cliExtractTar(t, b)
	if string(files["a.txt"]) != "A\n" {
		t.Errorf("a.txt = %q", files["a.txt"])
	}
	if string(files["sub/b.txt"]) != "B\n" {
		t.Errorf("sub/b.txt = %q", files["sub/b.txt"])
	}
	if _, ok := files[profile.ExportManifestName]; !ok {
		t.Error("manifest missing")
	}
}
