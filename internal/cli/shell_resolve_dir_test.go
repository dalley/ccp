//go:build !windows

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalley/ccp/internal/allowlist"
	"github.com/dalley/ccp/internal/paths"
)

// writeMarker is a hot-path test helper that writes a single-line marker
// with a trailing \n (the canonical form) at path.
func writeMarker(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// approveMarker writes the marker to disk then adds it to the allowlist
// at p.AllowlistPath. Mirrors the production flow of `ccp allow` without
// requiring the Unit 9 CLI (not yet implemented).
func approveMarker(t *testing.T, p paths.Paths, markerPath string) {
	t.Helper()
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := allowlist.Approve(p.AllowlistPath, markerPath); err != nil {
		t.Fatalf("Approve: %v", err)
	}
}

// TestShellResolveDirAllowedMarkerEmitsThreeLines exercises the happy
// path: a marker written under the hermetic CCP_ROOT, approved via
// allowlist.Approve, resolves to a three-line CCP_AUTO_* emission.
func TestShellResolveDirAllowedMarkerEmitsThreeLines(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarker(t, marker, "work\n")

	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	approveMarker(t, p, marker)

	out, _, err := runCLI(t, "", "shell-resolve-dir", repo)
	if err != nil {
		t.Fatalf("shell-resolve-dir: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), out)
	}
	if lines[0] != "CCP_AUTO_PROFILE='work'" {
		t.Errorf("line 0 = %q, want CCP_AUTO_PROFILE='work'", lines[0])
	}
	wantMarker := "CCP_AUTO_MARKER='" + marker + "'"
	if lines[1] != wantMarker {
		t.Errorf("line 1 = %q, want %q", lines[1], wantMarker)
	}
	if !strings.HasPrefix(lines[2], "CCP_AUTO_MARKER_MTIME='") || !strings.HasSuffix(lines[2], "'") {
		t.Errorf("line 2 = %q, want CCP_AUTO_MARKER_MTIME='<unix>'", lines[2])
	}
}

// TestShellResolveDirUnallowedMarker verifies the one-line
// CCP_AUTO_WARN='unallowed' emission when a marker exists but no
// allow-list entry does. No CCP_AUTO_PROFILE line must appear.
func TestShellResolveDirUnallowedMarker(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarker(t, marker, "work\n")

	out, _, err := runCLI(t, "", "shell-resolve-dir", repo)
	if err != nil {
		t.Fatalf("shell-resolve-dir: %v\n%s", err, out)
	}
	if out != "CCP_AUTO_WARN='unallowed'\n" {
		t.Errorf("output = %q, want CCP_AUTO_WARN='unallowed'\\n", out)
	}
	if strings.Contains(out, "CCP_AUTO_PROFILE") {
		t.Errorf("unallowed output must not include CCP_AUTO_PROFILE: %s", out)
	}
}

// TestShellResolveDirDriftMarker verifies CCP_AUTO_WARN='drift' when the
// allow-list has an entry but the content hash differs.
func TestShellResolveDirDriftMarker(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarker(t, marker, "work\n")

	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	approveMarker(t, p, marker)

	// Mutate the marker after approval — same valid name, different bytes.
	writeMarker(t, marker, "personal\n")

	out, _, err := runCLI(t, "", "shell-resolve-dir", repo)
	if err != nil {
		t.Fatalf("shell-resolve-dir: %v\n%s", err, out)
	}
	if out != "CCP_AUTO_WARN='drift'\n" {
		t.Errorf("output = %q, want CCP_AUTO_WARN='drift'\\n", out)
	}
}

// TestShellResolveDirNoMarker verifies empty stdout + exit 0 when no
// marker exists anywhere up the walk (FindMarker stops at $HOME).
func TestShellResolveDirNoMarker(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "shell-resolve-dir", repo)
	if err != nil {
		t.Fatalf("shell-resolve-dir: %v\n%s", err, out)
	}
	if out != "" {
		t.Errorf("output = %q, want empty", out)
	}
}

// TestShellResolveDirInvalidProfileName — a marker whose contents do
// not match the ValidateName grammar (uppercase, too long, etc.) must
// fail closed with empty stdout and exit 0. No CCP_AUTO_WARN, because
// the marker itself is malformed and we are not obligated to distinguish
// "malformed" from "absent" for the shell hook.
func TestShellResolveDirInvalidProfileName(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	// Capital letters violate the strict grammar.
	writeMarker(t, marker, "Invalid-Name\n")

	out, _, err := runCLI(t, "", "shell-resolve-dir", repo)
	if err != nil {
		t.Fatalf("shell-resolve-dir: %v\n%s", err, out)
	}
	if out != "" {
		t.Errorf("output = %q, want empty", out)
	}
}

// TestShellResolveDirSymlinkedMarkerSilentlySkips is the security
// invariant from Unit 10 / Key Decision 15: a symlinked marker emits
// NO CCP_AUTO_WARN — because doing so would create an existence oracle
// that an attacker could use to probe whether a user has a specific
// path. Do NOT change this test to expect a warning: the silent-skip
// on symlink is load-bearing.
func TestShellResolveDirSymlinkedMarkerSilentlySkips(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	target := filepath.Join(root, "elsewhere", ".claude-profile-source")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarker(t, target, "work\n")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, marker); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "shell-resolve-dir", repo)
	if err != nil {
		t.Fatalf("shell-resolve-dir: %v\n%s", err, out)
	}
	if out != "" {
		t.Errorf("symlinked marker must produce empty stdout (existence-oracle invariant), got %q", out)
	}
}

// TestShellResolveDirNonexistentDir verifies the hot-path discipline
// for a <dir> argument that doesn't exist — silent skip, exit 0.
func TestShellResolveDirNonexistentDir(t *testing.T) {
	root := setupCLI(t)
	out, _, err := runCLI(t, "", "shell-resolve-dir", filepath.Join(root, "does-not-exist"))
	if err != nil {
		t.Fatalf("shell-resolve-dir: %v\n%s", err, out)
	}
	if out != "" {
		t.Errorf("output = %q, want empty", out)
	}
}

// TestShellResolveDirEmptyDirArg — cobra.ExactArgs(1) accepts the empty
// string; the command must still silently skip on empty input.
func TestShellResolveDirEmptyDirArg(t *testing.T) {
	setupCLI(t)
	out, _, err := runCLI(t, "", "shell-resolve-dir", "")
	if err != nil {
		t.Fatalf("shell-resolve-dir: %v\n%s", err, out)
	}
	if out != "" {
		t.Errorf("output = %q, want empty", out)
	}
}

// TestShellResolveDirAllowlistMissing — no allowlist.toml on disk means
// every marker is treated as unallowed. Verifies hot-path doesn't error
// when the allowlist file hasn't been created yet (fresh machine).
func TestShellResolveDirAllowlistMissing(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarker(t, marker, "work\n")

	// Do NOT call approveMarker; allowlist.toml is absent.
	out, _, err := runCLI(t, "", "shell-resolve-dir", repo)
	if err != nil {
		t.Fatalf("shell-resolve-dir: %v\n%s", err, out)
	}
	if out != "CCP_AUTO_WARN='unallowed'\n" {
		t.Errorf("output = %q, want CCP_AUTO_WARN='unallowed'\\n", out)
	}
}

// TestShellResolveDirAllowlistUnreadable — chmod 0 on allowlist.toml
// makes it unreadable. Must silently exit 0 with empty stdout.
// Diagnostic path is `ccp allow --status` (Unit 9), not here.
func TestShellResolveDirAllowlistUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("cannot test permissions as root")
	}
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarker(t, marker, "work\n")

	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	approveMarker(t, p, marker)

	// Strip read perms on the allowlist. Restore at test exit so t.TempDir
	// cleanup can remove the file.
	if err := os.Chmod(p.AllowlistPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p.AllowlistPath, 0o600) })

	out, _, err := runCLI(t, "", "shell-resolve-dir", repo)
	if err != nil {
		t.Fatalf("shell-resolve-dir: %v\n%s", err, out)
	}
	// Allowlist unreadable → Check returns an error path; our hot-path
	// handler swallows it. Empty stdout is the correct answer.
	if out != "" {
		t.Errorf("output = %q, want empty when allowlist unreadable", out)
	}
}

// TestShellResolveDirMarkerPathWithMetacharacters checks that a marker
// sitting at a directory path loaded with shell metacharacters still
// produces output that parses correctly when eval'd in /bin/sh -c.
// This is the realest possible test for shellQuote: we actually run the
// emitted output through /bin/sh and read the resulting env vars back.
func TestShellResolveDirMarkerPathWithMetacharacters(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := setupCLI(t)
	// A dirname that exercises every cheap shell metacharacter at once.
	// Single quote, space, dollar, backtick, backslash. Newline and
	// control chars are excluded from the dir name because filesystems
	// tolerate them poorly and they would confuse the test harness itself.
	ugly := `has spaces and 'quote' $VAR ` + "`bt`" + ` back\slash`
	repo := filepath.Join(root, ugly)
	marker := filepath.Join(repo, ".claude-profile")
	writeMarker(t, marker, "work\n")

	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	approveMarker(t, p, marker)

	out, _, err := runCLI(t, "", "shell-resolve-dir", repo)
	if err != nil {
		t.Fatalf("shell-resolve-dir: %v\n%s", err, out)
	}
	// Pipe the emission through /bin/sh -c 'eval <out>; echo $CCP_AUTO_MARKER'.
	// A quoting bug in shellQuote would break the eval, surface as a
	// non-zero exit, or leave CCP_AUTO_MARKER empty / wrong.
	script := out + "\nprintf '%s\\n' \"$CCP_AUTO_MARKER\"\n"
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	shOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh eval of emitted output failed: %v\nemitted:\n%s\nsh stderr+stdout:\n%s", err, out, shOut)
	}
	got := strings.TrimSpace(string(shOut))
	if got != marker {
		t.Errorf("eval'd CCP_AUTO_MARKER = %q, want %q\nemitted:\n%s", got, marker, out)
	}
}

// TestShellResolveDirCompletesQuickly is a qualitative wall-clock check —
// a single invocation on a hermetic tree should return in <500ms. Go
// cold-start dominates and is platform-dependent, so we don't set a
// tight threshold; 500ms flags only truly pathological regressions.
func TestShellResolveDirCompletesQuickly(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarker(t, marker, "work\n")

	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	approveMarker(t, p, marker)

	start := time.Now()
	_, _, err = runCLI(t, "", "shell-resolve-dir", repo)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("shell-resolve-dir took %v, want <500ms (hot-path regression)", elapsed)
	}
}

// TestShellResolveDirWalksUpFromSubdir verifies the FindMarker walk-up
// semantics: a marker in an ancestor dir is found when invoked from a
// deeper sub-directory, subject to the .git and $HOME boundaries.
func TestShellResolveDirWalksUpFromSubdir(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	sub := filepath.Join(repo, "src", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(repo, ".claude-profile")
	writeMarker(t, marker, "work\n")

	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	approveMarker(t, p, marker)

	out, _, err := runCLI(t, "", "shell-resolve-dir", sub)
	if err != nil {
		t.Fatalf("shell-resolve-dir: %v\n%s", err, out)
	}
	if !strings.Contains(out, "CCP_AUTO_PROFILE='work'") {
		t.Errorf("walk-up didn't find ancestor marker, got:\n%s", out)
	}
	if !strings.Contains(out, "CCP_AUTO_MARKER='"+marker+"'") {
		t.Errorf("marker path missing from output:\n%s", out)
	}
}

// TestShellQuoteCorpus is a table-driven corpus of shell metacharacter
// combinations. Every entry is quoted, piped through /bin/sh -c,
// eval'd, and compared against the original. This is the authoritative
// test of shellQuote correctness — any future change to the function
// must keep every row green.
func TestShellQuoteCorpus(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"plain ascii", "hello"},
		{"space", "hello world"},
		{"single quote", "it's"},
		{"double single quote", "''"},
		{"dollar variable", "$HOME"},
		{"backtick", "`date`"},
		{"backslash", `back\slash`},
		{"newline", "line1\nline2"},
		{"tab", "col1\tcol2"},
		{"double quote", `say "hi"`},
		{"semicolon", "a;b"},
		{"ampersand", "a&b"},
		{"pipe", "a|b"},
		{"redirect", "a>b<c"},
		{"parens", "(subshell)"},
		{"braces", "${var}"},
		{"star glob", "*"},
		{"question glob", "???"},
		{"quote in dollar", `$'ansi-c'`},
		{"everything", `' " $ ` + "`" + ` \ ; & | > < ( ) { } * ?`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			quoted := shellQuote(tc.in)
			// Build a snippet that sets VAR=<quoted> then prints $VAR
			// with a trailing NUL sentinel so a trailing newline in
			// the input doesn't get stripped by $(...) semantics. Use
			// octal \000 — POSIX-portable; \xHH is a bash extension
			// dash (Ubuntu /bin/sh) does not interpret.
			script := "VAR=" + quoted + "\nprintf '%s\\000' \"$VAR\"\n"
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = []string{"PATH=/usr/bin:/bin"}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("sh eval of %q (quoted: %s) failed: %v\n%s", tc.in, quoted, err, out)
			}
			// Strip the \x00 sentinel.
			got := strings.TrimSuffix(string(out), "\x00")
			if got != tc.in {
				t.Errorf("round trip failed\ninput:   %q\nquoted:  %s\nroundtrip: %q", tc.in, quoted, got)
			}
		})
	}
}

// TestShellQuoteFixedExamples pins specific expected encodings of a few
// well-known cases so a future refactor can't silently change the
// representation in a way that breaks consumers that parse the output.
func TestShellQuoteFixedExamples(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "''"},
		{"abc", "'abc'"},
		{"it's", `'it'\''s'`},
		{"'", `''\'''`},
		{"a'b'c", `'a'\''b'\''c'`},
		{"$HOME", "'$HOME'"},
		{"a b", "'a b'"},
	}
	for _, tc := range cases {
		got := shellQuote(tc.in)
		if got != tc.want {
			t.Errorf("shellQuote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestShellResolveDirFishModeEmitsSetGX exercises the fish output path:
// the emission must use `set -gx NAME 'value'` syntax (valid in fish)
// rather than POSIX `NAME='value'` (which fish's eval rejects). This is
// the direct regression guard for Finding #3 — without --shell=fish the
// resolver emits POSIX, fish refuses to eval it, and CCP_AUTO_* are
// never set in fish sessions.
func TestShellResolveDirFishModeEmitsSetGX(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarker(t, marker, "work\n")

	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	approveMarker(t, p, marker)

	out, _, err := runCLI(t, "", "shell-resolve-dir", "--shell=fish", repo)
	if err != nil {
		t.Fatalf("shell-resolve-dir --shell=fish: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "set -gx CCP_AUTO_PROFILE 'work'") {
		t.Errorf("line 0 = %q, want prefix set -gx CCP_AUTO_PROFILE 'work'", lines[0])
	}
	if !strings.HasPrefix(lines[1], "set -gx CCP_AUTO_MARKER '"+marker+"'") {
		t.Errorf("line 1 = %q, want prefix set -gx CCP_AUTO_MARKER '<marker>'", lines[1])
	}
	if !strings.HasPrefix(lines[2], "set -gx CCP_AUTO_MARKER_MTIME '") {
		t.Errorf("line 2 = %q, want prefix set -gx CCP_AUTO_MARKER_MTIME '<unix>'", lines[2])
	}
}

// TestShellResolveDirFishModeDriftAndUnallowed verifies the warning
// emissions in fish mode match the one-line contract.
func TestShellResolveDirFishModeDriftAndUnallowed(t *testing.T) {
	root := setupCLI(t)

	// Unallowed (no allowlist entry).
	repoUnallowed := filepath.Join(root, "repo-unallowed")
	writeMarker(t, filepath.Join(repoUnallowed, ".claude-profile"), "work\n")
	out, _, err := runCLI(t, "", "shell-resolve-dir", "--shell=fish", repoUnallowed)
	if err != nil {
		t.Fatalf("shell-resolve-dir --shell=fish: %v", err)
	}
	if out != "set -gx CCP_AUTO_WARN 'unallowed'\n" {
		t.Errorf("unallowed output = %q, want set -gx CCP_AUTO_WARN 'unallowed'\\n", out)
	}

	// Drift (allowed then mutated).
	repoDrift := filepath.Join(root, "repo-drift")
	marker := filepath.Join(repoDrift, ".claude-profile")
	writeMarker(t, marker, "work\n")
	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	approveMarker(t, p, marker)
	writeMarker(t, marker, "personal\n")
	out, _, err = runCLI(t, "", "shell-resolve-dir", "--shell=fish", repoDrift)
	if err != nil {
		t.Fatalf("shell-resolve-dir --shell=fish drift: %v", err)
	}
	if out != "set -gx CCP_AUTO_WARN 'drift'\n" {
		t.Errorf("drift output = %q, want set -gx CCP_AUTO_WARN 'drift'\\n", out)
	}
}

// TestShellResolveDirFishModeLiveFishParses sources the emitted fish
// output through an actual fish binary and reads the resulting vars
// back. Skipped when fish isn't on PATH.
//
// Pairs with TestShellQuoteCorpus to give end-to-end coverage of fishQuote
// correctness: the round trip is the ultimate test for whether the quote
// rule matches what fish actually interprets.
func TestShellResolveDirFishModeLiveFishParses(t *testing.T) {
	if _, err := exec.LookPath("fish"); err != nil {
		t.Skip("fish not available")
	}
	root := setupCLI(t)
	// Use a dir name with fish-hostile metacharacters. Backslash and
	// single quote are the only two bytes fish single-quoting handles
	// specially; $ and backticks pass through literally (unlike POSIX).
	ugly := `it's \weird \path`
	repo := filepath.Join(root, ugly)
	marker := filepath.Join(repo, ".claude-profile")
	writeMarker(t, marker, "work\n")

	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	approveMarker(t, p, marker)

	out, _, err := runCLI(t, "", "shell-resolve-dir", "--shell=fish", repo)
	if err != nil {
		t.Fatalf("shell-resolve-dir: %v\n%s", err, out)
	}
	script := out + "\necho $CCP_AUTO_MARKER\n"
	cmd := exec.Command("fish", "-c", script)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin"}
	fishOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fish eval of emitted output failed: %v\nemitted:\n%s\nfish out:\n%s",
			err, out, fishOut)
	}
	got := strings.TrimSpace(string(fishOut))
	if got != marker {
		t.Errorf("fish-eval'd CCP_AUTO_MARKER = %q, want %q\nemitted:\n%s", got, marker, out)
	}
}

// TestFishQuoteRoundTrip is a table-driven corpus piped through fish
// itself. Mirrors TestShellQuoteCorpus for the fish emission path. If
// fish is not on PATH, skip — this is an integration test.
func TestFishQuoteRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("fish"); err != nil {
		t.Skip("fish not available")
	}
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"plain ascii", "hello"},
		{"space", "hello world"},
		{"single quote", "it's"},
		{"double single quote", "''"},
		{"backslash", `back\slash`},
		{"double backslash", `\\`},
		// fish single-quote-escape rule: $ and backticks are literal
		// inside single quotes, so these need no special treatment.
		{"dollar variable", "$HOME"},
		{"backtick", "`date`"},
		{"newline", "line1\nline2"},
		{"double quote", `say "hi"`},
		{"parens", "(subshell)"},
		{"everything fish-special", `' \ ' \ `},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			quoted := fishQuote(tc.in)
			script := "set -x VAR " + quoted + "\nprintf '%s\\x00' $VAR\n"
			cmd := exec.Command("fish", "-c", script)
			cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin"}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("fish eval of %q (quoted: %s) failed: %v\n%s",
					tc.in, quoted, err, out)
			}
			got := strings.TrimSuffix(string(out), "\x00")
			if got != tc.in {
				t.Errorf("round trip failed\ninput:    %q\nquoted:   %s\nroundtrip: %q",
					tc.in, quoted, got)
			}
		})
	}
}

// TestFishQuoteFixedExamples pins known encodings of selected inputs
// so a future refactor can't silently change the representation.
func TestFishQuoteFixedExamples(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "''"},
		{"abc", "'abc'"},
		{"it's", `'it\'s'`},
		// Backslash escaping: input `\` becomes `\\` inside the quotes.
		{`\`, `'\\'`},
		{`\'`, `'\\\''`},
		// `$` and backtick are literal inside fish single quotes.
		{"$HOME", "'$HOME'"},
		{"`date`", "'`date`'"},
	}
	for _, tc := range cases {
		got := fishQuote(tc.in)
		if got != tc.want {
			t.Errorf("fishQuote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestShellResolveDirUnknownShellIsSilent: invoking with an unrecognized
// --shell value must fail closed (empty stdout, exit 0) per the
// hot-path always-silent invariant.
func TestShellResolveDirUnknownShellIsSilent(t *testing.T) {
	root := setupCLI(t)
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(repo, ".claude-profile")
	writeMarker(t, marker, "work\n")
	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	approveMarker(t, p, marker)

	out, _, err := runCLI(t, "", "shell-resolve-dir", "--shell=powershell", repo)
	if err != nil {
		t.Fatalf("shell-resolve-dir --shell=powershell: %v", err)
	}
	if out != "" {
		t.Errorf("unknown shell must emit empty stdout, got %q", out)
	}
}

// TestShellResolveDirNoStderrEverEmitted asserts the always-silent-stderr
// invariant. A hot-path command must never print to stderr — the shell
// hook can't safely pipe it anywhere.
func TestShellResolveDirNoStderrEverEmitted(t *testing.T) {
	root := setupCLI(t)
	scenarios := []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{
			name: "no marker",
			setup: func(t *testing.T) string {
				d := filepath.Join(root, "empty-"+t.Name())
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
				return d
			},
		},
		{
			name: "malformed marker",
			setup: func(t *testing.T) string {
				d := filepath.Join(root, "bad-"+t.Name())
				writeMarker(t, filepath.Join(d, ".claude-profile"), "UPPERCASE\n")
				return d
			},
		},
		{
			name: "nonexistent dir",
			setup: func(t *testing.T) string {
				return filepath.Join(root, "does-not-exist-"+t.Name())
			},
		},
	}
	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			dir := sc.setup(t)
			_, stderr, err := runCLI(t, "", "shell-resolve-dir", dir)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if stderr != "" {
				t.Errorf("stderr must be empty on hot path, got %q", stderr)
			}
		})
	}
}
