//go:build !windows

package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dalley/ccp/internal/allowlist"
	"github.com/dalley/ccp/internal/paths"
)

func TestShellInitPosixContainsMarkersAndGuard(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)
	p, _ := paths.Resolve()

	var buf bytes.Buffer
	if err := writeShellInit(&buf, "zsh", p); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		shellInitBegin, shellInitEnd,
		`[ -n "$CLAUDE_CONFIG_DIR" ] && return 0`,
		`export CLAUDE_CONFIG_DIR="$HOME/.claude-$profile"`,
		`ccp shell-active`,
		// Unit 11 additions.
		`ccp shell-resolve-dir`,
		`CCP_AUTO_MARKER`,
		`CCP_AUTO_MARKER_MTIME`,
		`CCP_AUTO_PROFILE`,
		`CCP_AUTO_NOMARKER_ROOT`,
		`CCP_AUTO_WARN`,
		`CCP_PROFILE_AUTO`,
		`add-zsh-hook chpwd __ccp_activate`,
		`PROMPT_COMMAND`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("snippet missing %q. full output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "awk") {
		t.Errorf("snippet still uses awk; should go via ccp shell-active:\n%s", out)
	}
}

func TestShellInitFishContainsSetGX(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)
	p, _ := paths.Resolve()

	var buf bytes.Buffer
	if err := writeShellInit(&buf, "fish", p); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		`set -gx CLAUDE_CONFIG_DIR`,
		`--on-variable PWD`,
		`CCP_AUTO_MARKER`,
		`CCP_AUTO_WARN`,
		`ccp shell-resolve-dir`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("fish snippet missing %q, got:\n%s", want, out)
		}
	}
}

func TestShellInitUnsupportedShell(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)
	p, _ := paths.Resolve()

	var buf bytes.Buffer
	err := writeShellInit(&buf, "powershell", p)
	if err == nil {
		t.Fatal("expected error for unsupported shell")
	}
}

// TestShellInitPosixActuallyRunsInSh sources the snippet in a real /bin/sh
// and verifies it sets CLAUDE_CONFIG_DIR given CCP_PROFILE (legacy path).
func TestShellInitPosixActuallyRunsInSh(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)
	p, _ := paths.Resolve()

	var buf bytes.Buffer
	if err := writeShellInit(&buf, "zsh", p); err != nil {
		t.Fatal(err)
	}

	script := "set -e\nunset CLAUDE_CONFIG_DIR\nexport CCP_PROFILE=work\n" + buf.String() + "\nprintf '%s\\n' \"$CLAUDE_CONFIG_DIR\"\n"
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(cmd.Env, "HOME=/tmp/fake-home", "PATH=/usr/bin:/bin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh eval failed: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	if got != "/tmp/fake-home/.claude-work" {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want %q\nfull output:\n%s", got, "/tmp/fake-home/.claude-work", out)
	}
}

// -----------------------------------------------------------------------------
// Unit 11 live-sh integration tests for auto-activation.
//
// These tests exercise the POSIX snippet end-to-end by sourcing it in /bin/sh
// with a real `ccp` binary (or a shell shim) on PATH and asserting on the
// exported environment after a `cd` into (or around) a .claude-profile marker
// tree.
// -----------------------------------------------------------------------------

// realCCPOnce caches the path to a ccp binary built once per test package run.
// The build is optional — we only need it for tests that exercise the
// resolver fork path. Tests that only need the shell-side logic use a shim.
var (
	realCCPOnce sync.Once
	realCCPPath string
	realCCPErr  error
)

// buildRealCCP returns the path to a freshly-built ccp binary, or skips the
// test if `go` isn't available. Result cached for the package-level run.
func buildRealCCP(t *testing.T) string {
	t.Helper()
	realCCPOnce.Do(func() {
		if _, err := exec.LookPath("go"); err != nil {
			realCCPErr = err
			return
		}
		// Build into a stable per-test-package temp path so multiple tests
		// reuse it.
		dir, err := os.MkdirTemp("", "ccp-shellinit-bin-")
		if err != nil {
			realCCPErr = err
			return
		}
		out := filepath.Join(dir, "ccp")
		// Locate the module root by walking up from cwd looking for go.mod.
		// The test runs with cwd = internal/cli, so module root is two up.
		wd, err := os.Getwd()
		if err != nil {
			realCCPErr = err
			return
		}
		modRoot := wd
		for range []int{0, 1, 2, 3, 4} {
			if _, err := os.Stat(filepath.Join(modRoot, "go.mod")); err == nil {
				break
			}
			modRoot = filepath.Dir(modRoot)
		}
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", out, "./cmd/ccp")
		cmd.Dir = modRoot
		if output, err := cmd.CombinedOutput(); err != nil {
			realCCPErr = err
			realCCPPath = string(output) // for diagnostics
			return
		}
		realCCPPath = out
	})
	if realCCPErr != nil {
		t.Skipf("ccp build unavailable: %v (%s)", realCCPErr, realCCPPath)
	}
	return realCCPPath
}

// scenarioEnv is a bundle of directories a live-sh test operates in:
// a hermetic HOME, a hermetic CCP_ROOT, the snippet contents, and a PATH dir
// containing whatever ccp flavor (real or shim) the test needs.
type scenarioEnv struct {
	home    string // fake $HOME
	ccpRoot string // $CCP_ROOT for ccp binary
	pathDir string // dir containing a ccp binary/shim
	snippet string // shell-init output
}

// newScenarioEnv sets up a fresh hermetic test environment with a real ccp
// binary symlinked into pathDir. Returns nil if the build is unavailable.
func newScenarioEnv(t *testing.T) *scenarioEnv {
	t.Helper()
	ccpBin := buildRealCCP(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	ccpRoot := filepath.Join(root, "ccproot")
	pathDir := filepath.Join(root, "bin")
	for _, d := range []string{home, ccpRoot, pathDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Symlink ccp into pathDir so the snippet's `command -v ccp` finds it.
	if err := os.Symlink(ccpBin, filepath.Join(pathDir, "ccp")); err != nil {
		t.Fatal(err)
	}
	// Build the snippet with our hermetic CCP_ROOT so paths.Resolve agrees.
	t.Setenv("CCP_ROOT", ccpRoot)
	p, _ := paths.Resolve()
	var buf bytes.Buffer
	if err := writeShellInit(&buf, "zsh", p); err != nil {
		t.Fatal(err)
	}
	return &scenarioEnv{
		home:    home,
		ccpRoot: ccpRoot,
		pathDir: pathDir,
		snippet: buf.String(),
	}
}

// run executes a sh -c script with the hermetic env applied and returns
// stdout+stderr combined (via separate streams). The script has access to
// $SNIPPET (the snippet file path) to source it.
func (e *scenarioEnv) run(t *testing.T, script string, extraEnv ...string) (stdout, stderr string, err error) {
	t.Helper()
	snippetPath := filepath.Join(t.TempDir(), "snippet.sh")
	if err := os.WriteFile(snippetPath, []byte(e.snippet), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", script)
	env := []string{
		"HOME=" + e.home,
		"CCP_ROOT=" + e.ccpRoot,
		"PATH=" + e.pathDir + ":/usr/bin:/bin",
		"SNIPPET=" + snippetPath,
	}
	env = append(env, extraEnv...)
	cmd.Env = env
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// approveMarkerFile mirrors `ccp allow` without invoking the CLI.
func approveMarkerFile(t *testing.T, p paths.Paths, markerPath string) {
	t.Helper()
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := allowlist.Approve(p.AllowlistPath, markerPath); err != nil {
		t.Fatalf("Approve: %v", err)
	}
}

// TestShellInitAutoActivateAllowedMarker: cd into a subdir under an allowed
// marker → CLAUDE_CONFIG_DIR gets set to $HOME/.claude-<profile>.
func TestShellInitAutoActivateAllowedMarker(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	e := newScenarioEnv(t)

	// Seed a marker and approve it.
	repo := filepath.Join(e.home, "work-repo")
	marker := filepath.Join(repo, ".claude-profile")
	sub := filepath.Join(repo, "sub", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCP_ROOT", e.ccpRoot)
	p, _ := paths.Resolve()
	approveMarkerFile(t, p, marker)

	script := `set -e
unset CLAUDE_CONFIG_DIR
cd "` + sub + `"
. "$SNIPPET"
printf 'CONFIG=%s\n' "$CLAUDE_CONFIG_DIR"
printf 'MARKER=%s\n' "$CCP_AUTO_MARKER"
printf 'MPROF=%s\n' "$CCP_AUTO_PROFILE"
`
	stdout, stderr, err := e.run(t, script)
	if err != nil {
		t.Fatalf("sh failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if want := "CONFIG=" + e.home + "/.claude-work"; !strings.Contains(stdout, want) {
		t.Errorf("expected %q in stdout, got:\n%s\nstderr: %s", want, stdout, stderr)
	}
	if !strings.Contains(stdout, "MARKER="+marker) {
		t.Errorf("expected MARKER=%s in stdout, got:\n%s", marker, stdout)
	}
	if !strings.Contains(stdout, "MPROF=work") {
		t.Errorf("expected MPROF=work in stdout, got:\n%s", stdout)
	}
}

// TestShellInitAutoActivateDriftWarnsOnce: approve a marker, then mutate it;
// the hook must emit one stderr warning (drift) AND not set
// CLAUDE_CONFIG_DIR. A second __ccp_activate in the same shell must be
// silent (warn-once guard).
func TestShellInitAutoActivateDriftWarnsOnce(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	e := newScenarioEnv(t)
	repo := filepath.Join(e.home, "work-repo")
	marker := filepath.Join(repo, ".claude-profile")
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCP_ROOT", e.ccpRoot)
	p, _ := paths.Resolve()
	approveMarkerFile(t, p, marker)
	// Drift the marker content.
	if err := os.WriteFile(marker, []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	script := `set -e
unset CLAUDE_CONFIG_DIR
cd "` + sub + `"
. "$SNIPPET"
printf 'CONFIG1=%s\n' "$CLAUDE_CONFIG_DIR"
# Second activation on same marker should be silent.
cd "` + sub + `"
__ccp_activate
printf 'CONFIG2=%s\n' "$CLAUDE_CONFIG_DIR"
`
	stdout, stderr, err := e.run(t, script)
	if err != nil {
		t.Fatalf("sh failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if strings.Contains(stdout, "CONFIG1="+e.home+"/.claude-") {
		if !strings.HasPrefix(extractLine(stdout, "CONFIG1="), "CONFIG1=") {
			// fallthrough to generic check
		}
		// CONFIG1 should be empty (drift fails closed, no activation).
		line := extractLine(stdout, "CONFIG1=")
		if line != "CONFIG1=" {
			t.Errorf("expected CONFIG1 empty on drift, got %q\nstderr: %s", line, stderr)
		}
	}
	// First run: warning on stderr.
	if !strings.Contains(stderr, "drift") {
		t.Errorf("expected drift warning on stderr, got: %q", stderr)
	}
	if !strings.Contains(stderr, "ccp allow") {
		t.Errorf("expected 'ccp allow' hint in stderr, got: %q", stderr)
	}
	// Warn-once: only one drift line.
	driftCount := strings.Count(stderr, "drift")
	if driftCount != 1 {
		t.Errorf("expected exactly 1 drift warning, got %d: %q", driftCount, stderr)
	}
}

// extractLine returns the first line starting with prefix, or "".
func extractLine(s, prefix string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}

// TestShellInitAutoActivateNoMarkerFallsThroughToLegacy: no marker anywhere,
// but CCP_PROFILE is set → legacy path takes over; CLAUDE_CONFIG_DIR gets set
// from the legacy computation.
func TestShellInitAutoActivateNoMarkerFallsThroughToLegacy(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	e := newScenarioEnv(t)
	// Use a scratch dir that has no marker all the way up under $HOME.
	scratch := filepath.Join(e.home, "plain", "nested")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `set -e
unset CLAUDE_CONFIG_DIR
export CCP_PROFILE=alt
cd "` + scratch + `"
. "$SNIPPET"
printf 'CONFIG=%s\n' "$CLAUDE_CONFIG_DIR"
printf 'NOMARKER=%s\n' "$CCP_AUTO_NOMARKER_ROOT"
`
	stdout, stderr, err := e.run(t, script)
	if err != nil {
		t.Fatalf("sh failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if want := "CONFIG=" + e.home + "/.claude-alt"; !strings.Contains(stdout, want) {
		t.Errorf("expected %q, got:\n%s", want, stdout)
	}
	// Negative cache root should be populated.
	if !strings.Contains(stdout, "NOMARKER="+scratch) {
		t.Errorf("expected NOMARKER=%s, got:\n%s", scratch, stdout)
	}
}

// TestShellInitAutoActivateCLAUDECONFIGDIRSetBeforeSource: pre-set
// CLAUDE_CONFIG_DIR; hook must leave it untouched even with a marker in tree.
func TestShellInitAutoActivateCLAUDECONFIGDIRSetBeforeSource(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	e := newScenarioEnv(t)
	repo := filepath.Join(e.home, "r")
	marker := filepath.Join(repo, ".claude-profile")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCP_ROOT", e.ccpRoot)
	p, _ := paths.Resolve()
	approveMarkerFile(t, p, marker)

	script := `set -e
export CLAUDE_CONFIG_DIR=/preset/value
cd "` + repo + `"
. "$SNIPPET"
printf 'CONFIG=%s\n' "$CLAUDE_CONFIG_DIR"
`
	stdout, stderr, err := e.run(t, script)
	if err != nil {
		t.Fatalf("sh failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "CONFIG=/preset/value") {
		t.Errorf("pre-set CLAUDE_CONFIG_DIR got mutated, stdout:\n%s", stdout)
	}
}

// TestShellInitAutoActivateDisabledViaProfileAuto: CCP_PROFILE_AUTO=0 skips
// the auto layer; legacy CCP_PROFILE path still works.
func TestShellInitAutoActivateDisabledViaProfileAuto(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	e := newScenarioEnv(t)
	// Put an allowed marker in place — should be ignored because AUTO=0.
	repo := filepath.Join(e.home, "r")
	marker := filepath.Join(repo, ".claude-profile")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCP_ROOT", e.ccpRoot)
	p, _ := paths.Resolve()
	approveMarkerFile(t, p, marker)

	script := `set -e
unset CLAUDE_CONFIG_DIR
export CCP_PROFILE_AUTO=0
export CCP_PROFILE=legacyprofile
cd "` + repo + `"
. "$SNIPPET"
printf 'CONFIG=%s\n' "$CLAUDE_CONFIG_DIR"
printf 'MARKER=%s\n' "$CCP_AUTO_MARKER"
`
	stdout, stderr, err := e.run(t, script)
	if err != nil {
		t.Fatalf("sh failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if want := "CONFIG=" + e.home + "/.claude-legacyprofile"; !strings.Contains(stdout, want) {
		t.Errorf("expected %q (legacy path), got:\n%s", want, stdout)
	}
	if !strings.Contains(stdout, "MARKER=\n") && !strings.HasSuffix(strings.TrimSpace(extractLine(stdout, "MARKER=")), "=") {
		t.Errorf("CCP_AUTO_MARKER should be empty with AUTO=0, got:\n%s", stdout)
	}
}

// TestShellInitAutoActivateCacheHitNoFork: pre-populate
// CCP_AUTO_MARKER/MTIME/PROFILE; replace ccp on PATH with a shim that logs
// every invocation. The hook should NOT invoke ccp on the cache hit.
func TestShellInitAutoActivateCacheHitNoFork(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	// Build snippet against a hermetic CCP_ROOT — we don't need a real ccp
	// here because the cache-hit path shouldn't touch it.
	root := t.TempDir()
	home := filepath.Join(root, "home")
	pathDir := filepath.Join(root, "bin")
	callLog := filepath.Join(root, "ccp_calls.log")
	for _, d := range []string{home, pathDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Shim ccp that appends its args to callLog and prints nothing.
	shim := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + callLog + "\n"
	shimPath := filepath.Join(pathDir, "ccp")
	if err := os.WriteFile(shimPath, []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed a marker.
	repo := filepath.Join(home, "r")
	marker := filepath.Join(repo, ".claude-profile")
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	mtime := fi.ModTime().Unix()

	// Generate the snippet.
	t.Setenv("CCP_ROOT", filepath.Join(root, "ccproot"))
	p, _ := paths.Resolve()
	var buf bytes.Buffer
	if err := writeShellInit(&buf, "zsh", p); err != nil {
		t.Fatal(err)
	}
	snippetPath := filepath.Join(root, "snippet.sh")
	if err := os.WriteFile(snippetPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	script := `set -e
unset CLAUDE_CONFIG_DIR
export CCP_AUTO_MARKER=` + marker + `
export CCP_AUTO_MARKER_MTIME=` + itoaUnix(mtime) + `
export CCP_AUTO_PROFILE=work
cd "` + sub + `"
. "$SNIPPET"
printf 'CONFIG=%s\n' "$CLAUDE_CONFIG_DIR"
`
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{
		"HOME=" + home,
		"PATH=" + pathDir + ":/usr/bin:/bin",
		"SNIPPET=" + snippetPath,
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("sh failed: %v\nstdout: %s\nstderr: %s", err, outBuf.String(), errBuf.String())
	}
	if want := "CONFIG=" + home + "/.claude-work"; !strings.Contains(outBuf.String(), want) {
		t.Errorf("expected %q, got:\n%s", want, outBuf.String())
	}
	// The ccp shim MUST NOT have been called during the cache-hit path.
	// `command -v ccp` doesn't execute the binary (POSIX builtin), so only
	// actual invocations land in the log.
	data, _ := os.ReadFile(callLog)
	if len(bytes.TrimSpace(data)) != 0 {
		t.Errorf("expected zero ccp invocations on cache hit, got:\n%s", data)
	}
}

// TestShellInitFishSnippetParses exercises the fish snippet by asking fish
// itself to parse it. Skipped when fish isn't on PATH.
func TestShellInitFishSnippetParses(t *testing.T) {
	if _, err := exec.LookPath("fish"); err != nil {
		t.Skip("fish not available")
	}
	root := t.TempDir()
	t.Setenv("CCP_ROOT", root)
	p, _ := paths.Resolve()
	var buf bytes.Buffer
	if err := writeShellInit(&buf, "fish", p); err != nil {
		t.Fatal(err)
	}
	// Write to a temp file and ask fish to source it with a no-op PWD.
	snippetPath := filepath.Join(root, "snippet.fish")
	if err := os.WriteFile(snippetPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("fish", "-c", "source "+snippetPath+"; and echo OK")
	cmd.Env = append(cmd.Env, "HOME="+root, "PATH=/usr/bin:/bin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fish source failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "OK") {
		t.Errorf("fish snippet did not source cleanly, output:\n%s", out)
	}
}

// TestShellInitFishAutoActivateAllowedMarker is the fish counterpart to
// TestShellInitPosixActuallyRunsInSh and TestShellInitAutoActivateAllowedMarker.
// It stands up a hermetic env with a real ccp binary, seeds an approved
// marker, sources the fish snippet in a real fish shell, and verifies
// that CCP_AUTO_PROFILE / CCP_AUTO_MARKER / CLAUDE_CONFIG_DIR get set.
//
// Before the --shell=fish flag was added to `ccp shell-resolve-dir`, fish
// could never eval the POSIX `VAR='value'` output, so this test (had it
// existed) would have been silently broken. Keep it green as regression
// protection for the fish auto-activation path.
func TestShellInitFishAutoActivateAllowedMarker(t *testing.T) {
	if _, err := exec.LookPath("fish"); err != nil {
		t.Skip("fish not available")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not available to build ccp")
	}
	// Piggyback on the POSIX scenario plumbing (build ccp once, hermetic
	// HOME/CCP_ROOT, pathDir with a ccp symlink), but generate a fish
	// snippet instead of a POSIX one.
	ccpBin := buildRealCCP(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	ccpRoot := filepath.Join(root, "ccproot")
	pathDir := filepath.Join(root, "bin")
	for _, d := range []string{home, ccpRoot, pathDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(ccpBin, filepath.Join(pathDir, "ccp")); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CCP_ROOT", ccpRoot)
	p, _ := paths.Resolve()
	var buf bytes.Buffer
	if err := writeShellInit(&buf, "fish", p); err != nil {
		t.Fatal(err)
	}
	snippetPath := filepath.Join(root, "snippet.fish")
	if err := os.WriteFile(snippetPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	// Seed + approve marker.
	repo := filepath.Join(home, "work-repo")
	marker := filepath.Join(repo, ".claude-profile")
	sub := filepath.Join(repo, "sub", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	approveMarkerFile(t, p, marker)

	// fish needs `cd`, the snippet source, and the PWD-change hook to
	// fire; we call __ccp_activate directly after cd so we don't depend
	// on fish's event-dispatch timing from within `-c`.
	script := "cd " + sub + "\n" +
		"source " + snippetPath + "\n" +
		"__ccp_activate\n" +
		"echo CONFIG=$CLAUDE_CONFIG_DIR\n" +
		"echo MARKER=$CCP_AUTO_MARKER\n" +
		"echo MPROF=$CCP_AUTO_PROFILE\n"
	cmd := exec.Command("fish", "-c", script)
	cmd.Env = []string{
		"HOME=" + home,
		"CCP_ROOT=" + ccpRoot,
		"PATH=" + pathDir + ":/usr/bin:/bin:/usr/local/bin",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fish script failed: %v\n%s", err, out)
	}
	got := string(out)
	if want := "CONFIG=" + home + "/.claude-work"; !strings.Contains(got, want) {
		t.Errorf("missing %q in fish output:\n%s", want, got)
	}
	if !strings.Contains(got, "MARKER="+marker) {
		t.Errorf("missing MARKER=%s in fish output:\n%s", marker, got)
	}
	if !strings.Contains(got, "MPROF=work") {
		t.Errorf("missing MPROF=work in fish output:\n%s", got)
	}
}

// itoaUnix renders an int64 unix time as decimal without importing strconv.
func itoaUnix(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
