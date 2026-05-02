//go:build !windows

package allowlist

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// writeMarker is the happy-path test helper — writes a single-line
// marker with trailing \n (the canonical form) at path.
func writeMarker(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// allowlistPath returns a fresh per-test allowlist path. The file does
// not yet exist; Load/Save create it on first Save.
func allowlistPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "allowlist.toml")
}

func TestLoadMissingReturnsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allowlist.toml")
	f, existed, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if existed {
		t.Errorf("existed = true, want false")
	}
	if f.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", f.SchemaVersion, CurrentSchemaVersion)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := allowlistPath(t)
	in := File{
		SchemaVersion: CurrentSchemaVersion,
		Entries: map[string]string{
			"/Users/alice/repo-a/.claude-profile": "sha256:aaaa",
			"/Users/alice/repo-b/.claude-profile": "sha256:bbbb",
		},
	}
	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, existed, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !existed {
		t.Fatal("existed = false after Save")
	}
	if got.SchemaVersion != in.SchemaVersion || len(got.Entries) != len(in.Entries) {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, in)
	}
	for k, v := range in.Entries {
		if got.Entries[k] != v {
			t.Errorf("entry %q: got %q, want %q", k, got.Entries[k], v)
		}
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allowlist.toml")
	if err := Save(path, Default()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "allowlist.toml" {
			t.Errorf("unexpected leftover %q", e.Name())
		}
	}
}

func TestSaveIs0600(t *testing.T) {
	path := allowlistPath(t)
	if err := Save(path, Default()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestLoadRejectsFutureSchema(t *testing.T) {
	path := allowlistPath(t)
	if err := os.WriteFile(path, []byte("schema_version = 9999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("expected schema-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "upgrade ccp") {
		t.Errorf("err = %v; want 'upgrade ccp' hint", err)
	}
}

func TestHashContentOnly(t *testing.T) {
	// Two markers at different paths with identical contents must hash
	// the same. This is the deliberate cross-machine-sync tradeoff
	// (Key Decision 12 in the v2.0 plan): including the path in the
	// hash would break re-approval-free sync.
	dir := t.TempDir()
	a := filepath.Join(dir, "a", ".claude-profile")
	b := filepath.Join(dir, "b", ".claude-profile")
	writeMarker(t, a, "work\n")
	writeMarker(t, b, "work\n")

	ha, err := Hash(a)
	if err != nil {
		t.Fatalf("Hash(a): %v", err)
	}
	hb, err := Hash(b)
	if err != nil {
		t.Fatalf("Hash(b): %v", err)
	}
	if ha != hb {
		t.Errorf("content-only hash mismatch: %q vs %q", ha, hb)
	}
	if !strings.HasPrefix(ha, "sha256:") {
		t.Errorf("hash %q missing sha256: prefix", ha)
	}
}

func TestHashDiffersOnContentChange(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude-profile")
	writeMarker(t, p, "work\n")
	h1, err := Hash(p)
	if err != nil {
		t.Fatal(err)
	}
	writeMarker(t, p, "personal\n")
	h2, err := Hash(p)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Errorf("hash did not change after content change: %s", h1)
	}
}

func TestHashRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	writeMarker(t, target, "work\n")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	_, err := Hash(link)
	if err == nil {
		t.Fatal("expected Hash(symlink) to fail, got nil")
	}
	// The error should name the refusal — use a loose check so we
	// don't tie to exact OS errno wording.
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("err = %v; want mention of symlink", err)
	}
}

func TestHashRejectsOversize(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude-profile")
	// 65 KiB of zeros — one byte over the cap.
	big := make([]byte, maxMarkerBytes+1)
	if err := os.WriteFile(p, big, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Hash(p)
	if err == nil {
		t.Fatal("expected oversize rejection, got nil")
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Errorf("err = %v; want mention of cap", err)
	}
}

func TestHashRejectsNonRegular(t *testing.T) {
	// Directories and devices should be refused — the fd opened fine
	// but fstat tells us it's not a regular file.
	dir := t.TempDir()
	_, err := Hash(dir)
	if err == nil {
		t.Fatal("expected non-regular-file rejection, got nil")
	}
}

func TestReadNameValid(t *testing.T) {
	cases := []struct{ content, want string }{
		{"work\n", "work"},
		{"work", "work"}, // missing trailing \n is allowed
		{"a\n", "a"},     // one-character minimum
		{"a" + strings.Repeat("b", 62) + "\n", "a" + strings.Repeat("b", 62)},
		{"my-profile-1_test\n", "my-profile-1_test"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, ".claude-profile")
			writeMarker(t, p, tc.content)
			got, err := ReadName(p)
			if err != nil {
				t.Fatalf("ReadName: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReadNameRejects covers every malformed-marker edge the plan lists.
// Each row asserts ErrInvalidMarker wrapping + a byte-offset mention.
func TestReadNameRejects(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantByte string // substring of the byte-offset clause we expect
	}{
		{"BOM prefix", "\xEF\xBB\xBFwork\n", "offset 0"},
		{"CRLF line ending", "work\r\n", "offset 4"},
		{"bare CR", "work\r", "offset 4"},
		{"leading whitespace", " work\n", "offset 0"},
		{"trailing space", "work \n", "offset 4"},
		{"trailing tab", "work\t\n", "offset 4"},
		{"multi-line", "work\nextra\n", "offset 4"},
		{"zero-width space prefix", "​work\n", "offset 0"},
		{"empty", "", ""},            // empty is rejected but byte-offset clause differs
		{"just a newline", "\n", ""}, // trailing-\n stripped leaves empty
		{"leading digit", "1work\n", "offset 0"},
		{"uppercase", "Work\n", "offset 0"},
		{"dot in name", "work.v2\n", "offset 4"},
		{"space in name", "work profile\n", "offset 4"},
		{"unicode letter", "wörk\n", "offset 1"},
		{"too long", strings.Repeat("a", 64) + "\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, ".claude-profile")
			writeMarker(t, p, tc.content)
			_, err := ReadName(p)
			if err == nil {
				t.Fatalf("ReadName(%q) succeeded, want error", tc.content)
			}
			if !errors.Is(err, ErrInvalidMarker) {
				t.Errorf("err = %v; want ErrInvalidMarker wrap", err)
			}
			if tc.wantByte != "" && !strings.Contains(err.Error(), tc.wantByte) {
				t.Errorf("err = %v; want %q in message", err, tc.wantByte)
			}
		})
	}
}

func TestReadNameRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	writeMarker(t, target, "work\n")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := ReadName(link)
	if err == nil {
		t.Fatal("want error on symlink, got nil")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("err = %v; want mention of symlink", err)
	}
}

func TestApproveCheckRevokeCycle(t *testing.T) {
	dir := t.TempDir()
	al := filepath.Join(dir, "allowlist.toml")
	m := filepath.Join(dir, "repo", ".claude-profile")
	writeMarker(t, m, "work\n")

	// Initial state: Unallowed + ErrMarkerNotAllowed.
	status, _, err := Check(al, m)
	if status != StatusUnallowed {
		t.Errorf("initial Check = %v, want Unallowed", status)
	}
	if !errors.Is(err, ErrMarkerNotAllowed) {
		t.Errorf("initial Check err = %v; want ErrMarkerNotAllowed", err)
	}

	// Approve, then Check → Allowed.
	if err := Approve(al, m); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	status, hash, err := Check(al, m)
	if err != nil {
		t.Fatalf("Check after Approve: %v", err)
	}
	if status != StatusAllowed {
		t.Errorf("Check = %v, want Allowed", status)
	}
	if !strings.HasPrefix(hash, "sha256:") {
		t.Errorf("hash = %q; want sha256: prefix", hash)
	}

	// Modify the marker → HashMismatch.
	writeMarker(t, m, "personal\n")
	status, _, err = Check(al, m)
	if status != StatusHashMismatch {
		t.Errorf("Check after edit = %v, want HashMismatch", status)
	}
	if !errors.Is(err, ErrMarkerHashMismatch) {
		t.Errorf("err = %v; want ErrMarkerHashMismatch", err)
	}

	// Revoke → Unallowed again.
	if err := Revoke(al, m); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	status, _, _ = Check(al, m)
	if status != StatusUnallowed {
		t.Errorf("Check after Revoke = %v, want Unallowed", status)
	}
}

func TestRevokeIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	al := filepath.Join(dir, "allowlist.toml")
	m := filepath.Join(dir, ".claude-profile")
	writeMarker(t, m, "work\n")

	// Revoke on a non-existent allowlist file.
	if err := Revoke(al, m); err != nil {
		t.Errorf("Revoke(missing file): %v", err)
	}
	// Revoke again after the Save created the file with no matching entry.
	if err := Approve(al, m); err != nil {
		t.Fatal(err)
	}
	if err := Revoke(al, filepath.Join(dir, "other", ".claude-profile")); err != nil {
		t.Errorf("Revoke(no-such-entry): %v", err)
	}
}

func TestCheckHandlesDanglingPath(t *testing.T) {
	// An entry for a path that no longer exists: Check should return a
	// clear error-with-context, not panic. Specifically, Hash fails
	// first (the file doesn't exist), and that failure surfaces with
	// the path in the message.
	dir := t.TempDir()
	al := filepath.Join(dir, "allowlist.toml")
	ghost := filepath.Join(dir, "ghost", ".claude-profile")
	// Hand-write an entry pointing at a non-existent file.
	if err := os.WriteFile(al, []byte(
		`schema_version = 1
[entries]
"`+ghost+`" = "sha256:deadbeef"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Check(al, ghost)
	if err == nil {
		t.Fatal("want error on dangling path, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("err = %v; want path in message", err)
	}
}

func TestFindMarkerHappy(t *testing.T) {
	dir := t.TempDir()
	// Place a marker three levels up from start.
	root := filepath.Join(dir, "root")
	start := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, ".claude-profile")
	writeMarker(t, marker, "work\n")

	got, err := FindMarker(start, dir)
	if err != nil {
		t.Fatalf("FindMarker: %v", err)
	}
	if got != marker {
		t.Errorf("got %q, want %q", got, marker)
	}
}

func TestFindMarkerNearestWins(t *testing.T) {
	// Marker closer to start wins over marker farther up.
	dir := t.TempDir()
	far := filepath.Join(dir, ".claude-profile")
	writeMarker(t, far, "work\n")
	near := filepath.Join(dir, "a", ".claude-profile")
	writeMarker(t, near, "other\n")
	start := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindMarker(start, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != near {
		t.Errorf("got %q, want %q", got, near)
	}
}

func TestFindMarkerStopsAtHome(t *testing.T) {
	// Marker lives above $HOME; FindMarker must not reach it.
	dir := t.TempDir()
	above := filepath.Join(dir, ".claude-profile")
	writeMarker(t, above, "work\n")
	home := filepath.Join(dir, "home")
	start := filepath.Join(home, "proj")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindMarker(start, home)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("FindMarker walked past $HOME to %q", got)
	}
}

func TestFindMarkerStopsAtGit(t *testing.T) {
	// Marker above the .git dir must not be found.
	dir := t.TempDir()
	above := filepath.Join(dir, ".claude-profile")
	writeMarker(t, above, "work\n")
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := filepath.Join(repo, "src")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindMarker(start, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("FindMarker walked past .git to %q", got)
	}
}

func TestFindMarkerNoneFound(t *testing.T) {
	dir := t.TempDir()
	start := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindMarker(start, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestFindMarkerAncestorCap(t *testing.T) {
	// Build a chain deeper than maxAncestors and confirm FindMarker
	// gives up cleanly without finding a marker placed at the top.
	dir := t.TempDir()
	writeMarker(t, filepath.Join(dir, ".claude-profile"), "work\n")
	// Build 80 levels of nesting.
	deep := dir
	for i := 0; i < 80; i++ {
		deep = filepath.Join(deep, fmt.Sprintf("d%d", i))
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	// homeDir left empty — we rely entirely on the depth cap here.
	got, err := FindMarker(deep, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("got %q; depth cap should have stopped the walk", got)
	}
}

// TestApproveConcurrentDifferentPaths verifies that concurrent Approve
// calls (serialized by a caller-supplied mutex, since the package
// intentionally does not lock) produce a final file containing BOTH
// entries — no lost-update corruption.
func TestApproveConcurrentDifferentPaths(t *testing.T) {
	dir := t.TempDir()
	al := filepath.Join(dir, "allowlist.toml")
	a := filepath.Join(dir, "a", ".claude-profile")
	b := filepath.Join(dir, "b", ".claude-profile")
	writeMarker(t, a, "work\n")
	writeMarker(t, b, "other\n")

	// The test supplies the locking the package doesn't — matches how
	// the real CLI wraps Approve with withLock(p, fn).
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, m := range []string{a, b} {
		m := m
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			if err := Approve(al, m); err != nil {
				t.Errorf("Approve(%s): %v", m, err)
			}
		}()
	}
	wg.Wait()

	f, _, err := Load(al)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Entries[a]; !ok {
		t.Errorf("missing entry for a; got %+v", f.Entries)
	}
	if _, ok := f.Entries[b]; !ok {
		t.Errorf("missing entry for b; got %+v", f.Entries)
	}
}

// TestCheckLockFreeConsistency runs a concurrent Approve/Revoke loop in
// the background and calls Check repeatedly from a reader goroutine. The
// invariant: every Check result must be one of the well-formed states
// (Allowed with matching hash, Unallowed, or HashMismatch) — never a
// torn-file parse error.
func TestCheckLockFreeConsistency(t *testing.T) {
	dir := t.TempDir()
	al := filepath.Join(dir, "allowlist.toml")
	m := filepath.Join(dir, ".claude-profile")
	writeMarker(t, m, "work\n")
	// Seed the allowlist so the first reader iteration sees a real file.
	if err := Save(al, Default()); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var mu sync.Mutex
	var writerErr atomic.Value

	// Writer: flip Approve/Revoke in a tight loop under a mutex. The
	// mutex models the CLI's withLock discipline; the reader goroutine
	// does NOT take it — that's the invariant we want to prove.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			mu.Lock()
			var err error
			if i%2 == 0 {
				err = Approve(al, m)
			} else {
				err = Revoke(al, m)
			}
			mu.Unlock()
			if err != nil {
				writerErr.Store(err)
				return
			}
		}
	}()

	// Reader: hammer Check for a fixed number of iterations and verify
	// no iteration sees a torn file (Load parse error).
	const iters = 500
	for i := 0; i < iters; i++ {
		status, _, err := Check(al, m)
		// Acceptable errors: ErrMarkerNotAllowed, ErrMarkerHashMismatch.
		// Unacceptable: anything else, which would indicate a torn read.
		if err != nil && !errors.Is(err, ErrMarkerNotAllowed) && !errors.Is(err, ErrMarkerHashMismatch) {
			t.Fatalf("Check saw unexpected error (torn read?): %v", err)
		}
		switch status {
		case StatusAllowed, StatusUnallowed, StatusHashMismatch:
			// ok
		default:
			t.Fatalf("Check returned non-enum status %d", status)
		}
	}
	close(stop)
	wg.Wait()
	if err := writerErr.Load(); err != nil {
		t.Fatalf("writer: %v", err)
	}
}
