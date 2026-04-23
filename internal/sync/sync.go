// Package sync backs ccp's `sync` commands. A ccp sync repo is just a plain
// git repo rooted at ~/.config/ccp/ whose tracked content is the `profiles/`
// directory plus a machine-identifying `.ccp-sync.json` marker. Everything
// else in ~/.config/ccp/ (manifest.toml, backups/, lock, secrets/) is
// .gitignore'd because it is either machine-local state or secret.
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// NetworkTimeout is the ceiling for any single remote operation. It balances
// tolerating slow networks against letting a wedged remote pin ccp forever.
// Override per-call via the CCP_NETWORK_TIMEOUT env var (Go duration format).
const NetworkTimeout = 60 * time.Second

func networkCtx() (context.Context, context.CancelFunc) {
	d := NetworkTimeout
	if v := os.Getenv("CCP_NETWORK_TIMEOUT"); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil && parsed > 0 {
			d = parsed
		}
	}
	return context.WithTimeout(context.Background(), d)
}

// SyncMarkerFilename is a small JSON file at the root of a ccp sync repo
// that identifies it as a ccp-managed repo. `sync setup` checks for it when
// bonding with an existing remote to avoid accidentally stomping an
// unrelated repo (same idea as jean-claude's `meta.json.managedBy`).
const SyncMarkerFilename = ".ccp-sync.json"

// GitignoreContents is what `sync setup` writes to the repo's .gitignore.
// Every entry is a path that must NEVER end up in Git: either machine-local,
// a cache, or a secret.
const GitignoreContents = `# ccp-managed. Do not remove the entries below.
/manifest.toml
/backups/
/lock
/allowlist.toml
/secrets/
/runtime-manifest/
.DS_Store
`

// Marker is the data stored in .ccp-sync.json.
type Marker struct {
	ManagedBy string `json:"managedBy"` // always "ccp"
	Version   int    `json:"version"`   // marker schema version
	Created   string `json:"created"`   // RFC-3339 timestamp
}

// currentMarkerVersion bumps when the marker schema changes. Readers accept
// any version ≤ their own.
const currentMarkerVersion = 1

// IsSyncRepo reports whether configDir looks like a ccp-managed sync repo.
func IsSyncRepo(configDir string) (bool, error) {
	info, err := os.Stat(filepath.Join(configDir, ".git"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf(".git is not a directory")
	}
	return true, nil
}

// ReadMarker reads .ccp-sync.json from the working tree. Missing file yields
// (nil, nil) — caller decides whether that's fatal.
func ReadMarker(configDir string) (*Marker, error) {
	b, err := os.ReadFile(filepath.Join(configDir, SyncMarkerFilename))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse marker: %w", err)
	}
	return &m, nil
}

// WriteMarker writes a fresh marker file.
func WriteMarker(configDir string) error {
	m := Marker{
		ManagedBy: "ccp",
		Version:   currentMarkerVersion,
		Created:   time.Now().UTC().Format(time.RFC3339),
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(configDir, SyncMarkerFilename), append(b, '\n'), 0o644)
}

// InitRepo initializes a git repo at configDir (if absent), writes the
// gitignore and marker, and makes an initial commit.
func InitRepo(configDir string) error {
	gitignore := filepath.Join(configDir, ".gitignore")
	if err := os.WriteFile(gitignore, []byte(GitignoreContents), 0o644); err != nil {
		return fmt.Errorf("write gitignore: %w", err)
	}
	if err := WriteMarker(configDir); err != nil {
		return err
	}

	existing, err := IsSyncRepo(configDir)
	if err != nil {
		return err
	}
	var repo *git.Repository
	if existing {
		repo, err = git.PlainOpen(configDir)
	} else {
		repo, err = git.PlainInitWithOptions(configDir, &git.PlainInitOptions{
			InitOptions: git.InitOptions{DefaultBranch: plumbing.Main},
			Bare:        false,
		})
	}
	if err != nil {
		return fmt.Errorf("open/init repo: %w", err)
	}

	w, err := repo.Worktree()
	if err != nil {
		return err
	}
	// Stage profiles/, gitignore, marker, and any existing tracked files.
	// filepath.Walk isn't strictly needed — Add with "." matches everything
	// not gitignored.
	if _, err := w.Add(".gitignore"); err != nil {
		return fmt.Errorf("stage gitignore: %w", err)
	}
	if _, err := w.Add(SyncMarkerFilename); err != nil {
		return fmt.Errorf("stage marker: %w", err)
	}
	// Add profiles/ if it exists and has content.
	if _, err := os.Stat(filepath.Join(configDir, "profiles")); err == nil {
		if _, err := w.Add("profiles"); err != nil {
			return fmt.Errorf("stage profiles: %w", err)
		}
	}

	status, err := w.Status()
	if err != nil {
		return err
	}
	if status.IsClean() {
		return nil // nothing new to commit
	}

	_, err = w.Commit("ccp sync init", &git.CommitOptions{
		Author: signature(),
	})
	if err != nil {
		return fmt.Errorf("initial commit: %w", err)
	}
	return nil
}

// SetRemote sets origin to url, replacing any prior origin remote.
func SetRemote(configDir, url string) error {
	repo, err := git.PlainOpen(configDir)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}
	// Drop any existing origin, ignore "not found".
	_ = repo.DeleteRemote("origin")
	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	})
	return err
}

// Remote returns the first URL of the origin remote, or "" if none.
func Remote(configDir string) (string, error) {
	repo, err := git.PlainOpen(configDir)
	if err != nil {
		return "", err
	}
	r, err := repo.Remote("origin")
	if err != nil {
		if err == git.ErrRemoteNotFound {
			return "", nil
		}
		return "", err
	}
	urls := r.Config().URLs
	if len(urls) == 0 {
		return "", nil
	}
	return urls[0], nil
}

// StageAndCommit stages profiles/ plus marker/gitignore and commits any
// pending changes. Returns (committed, err) — committed=false means the
// working tree was clean.
func StageAndCommit(configDir string) (bool, error) {
	repo, err := git.PlainOpen(configDir)
	if err != nil {
		return false, err
	}
	w, err := repo.Worktree()
	if err != nil {
		return false, err
	}
	for _, path := range []string{".gitignore", SyncMarkerFilename, "profiles"} {
		full := filepath.Join(configDir, path)
		if _, err := os.Stat(full); err != nil {
			continue
		}
		if _, err := w.Add(path); err != nil {
			return false, fmt.Errorf("stage %s: %w", path, err)
		}
	}
	status, err := w.Status()
	if err != nil {
		return false, err
	}
	if status.IsClean() {
		return false, nil
	}
	host, _ := os.Hostname()
	msg := fmt.Sprintf("Update from %s at %s", host, time.Now().UTC().Format(time.RFC3339))
	if _, err := w.Commit(msg, &git.CommitOptions{Author: signature()}); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

// Push pushes the default branch to origin. Dry-run is expressed at the CLI
// layer by short-circuiting before calling Push, not as a parameter here.
func Push(configDir string) error {
	repo, err := git.PlainOpen(configDir)
	if err != nil {
		return err
	}
	ctx, cancel := networkCtx()
	defer cancel()
	if err := repo.PushContext(ctx, &git.PushOptions{
		RemoteName: "origin",
		Auth:       sshAuthFromEnv(),
	}); err != nil {
		return wrapNetwork(err)
	}
	return nil
}

// IsDirty reports whether the working tree has uncommitted changes.
func IsDirty(configDir string) (bool, error) {
	repo, err := git.PlainOpen(configDir)
	if err != nil {
		return false, err
	}
	w, err := repo.Worktree()
	if err != nil {
		return false, err
	}
	status, err := w.Status()
	if err != nil {
		return false, err
	}
	return !status.IsClean(), nil
}

// PullOptions controls Pull.
type PullOptions struct {
	// Force: refused-by-default behavior is to abort on a dirty working
	// tree. Force backs up the profiles/ dir into backupDir and performs a
	// hard reset + pull.
	Force     bool
	BackupDir string
}

// Pull fetches + merges origin/<current branch>. Non-destructive by default:
// if the working tree is dirty, it returns an error rather than clobbering.
// Returns (changed, err) — changed=false means already up to date.
func Pull(configDir string, opts PullOptions) (bool, error) {
	if opts.Force && opts.BackupDir == "" {
		return false, fmt.Errorf("force pull requires a non-empty BackupDir")
	}
	dirty, err := IsDirty(configDir)
	if err != nil {
		return false, err
	}
	if dirty {
		if !opts.Force {
			return false, ErrDirtyWorkingTree
		}
		if err := backupProfilesDir(configDir, opts.BackupDir); err != nil {
			return false, fmt.Errorf("pre-pull backup: %w", err)
		}
		if err := hardReset(configDir); err != nil {
			return false, err
		}
	}

	repo, err := git.PlainOpen(configDir)
	if err != nil {
		return false, err
	}
	w, err := repo.Worktree()
	if err != nil {
		return false, err
	}
	ctx, cancel := networkCtx()
	defer cancel()
	err = w.PullContext(ctx, &git.PullOptions{
		RemoteName: "origin",
		Auth:       sshAuthFromEnv(),
	})
	switch {
	case err == nil:
		return true, nil
	case err == git.NoErrAlreadyUpToDate:
		return false, nil
	default:
		return false, wrapNetwork(err)
	}
}

// wrapNetwork decorates network-class errors from go-git with
// ErrRemoteUnreachable so the CLI maps them to ExitNetwork. Leaves other
// errors (auth, protocol, disk) alone — auth failures are user-class.
//
// This is string-based because go-git does not expose typed network-class
// sentinels for most failures. The hint list errs on the side of broad
// coverage; a false-positive (mis-classifying an auth error as network)
// costs an agent one unnecessary retry, whereas a false-negative leaks a
// network failure into ExitUser and breaks retry logic entirely.
func wrapNetwork(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	netHints := []string{
		"no such host",
		"connection refused",
		"connection reset",
		"network is unreachable",
		"no route to host",
		"server misbehaving", // systemd-resolved DNS failure on Linux
		"tls handshake timeout",
		"timeout",
		"i/o timeout",
		"unreachable",
		"context deadline",
		"context canceled",
	}
	for _, h := range netHints {
		if strings.Contains(msg, h) {
			return fmt.Errorf("%w: %v", ErrRemoteUnreachable, err)
		}
	}
	return err
}

// backupProfilesDir copies profiles/ into backupDir (retained by the caller's
// backup.Prune). Safe fall-through if profiles/ doesn't exist.
func backupProfilesDir(configDir, backupDir string) error {
	src := filepath.Join(configDir, "profiles")
	if _, err := os.Stat(src); err != nil {
		return nil
	}
	dst := filepath.Join(backupDir, "profiles")
	return copyDir(src, dst)
}

func hardReset(configDir string) error {
	repo, err := git.PlainOpen(configDir)
	if err != nil {
		return err
	}
	w, err := repo.Worktree()
	if err != nil {
		return err
	}
	head, err := repo.Head()
	if err != nil {
		// A freshly-initialized repo with zero commits has no HEAD. There
		// is nothing to reset TO; surface a clear message instead of the
		// raw go-git sentinel.
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return fmt.Errorf("repo has no commits yet; nothing to reset")
		}
		return err
	}
	return w.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: head.Hash()})
}

// StatusSummary is what `ccp sync status` reports.
type StatusSummary struct {
	Remote        string   `json:"remote"`
	RepoExists    bool     `json:"repo_exists"`
	Dirty         bool     `json:"dirty"`
	ChangedFiles  []string `json:"changed_files"`
	CurrentBranch string   `json:"current_branch,omitempty"`
	// Ahead / Behind commit counts against origin/<branch>. Populated only
	// when --fetch was passed; zero values when the caller didn't request a
	// network refresh.
	Ahead   int  `json:"ahead"`
	Behind  int  `json:"behind"`
	Fetched bool `json:"fetched"`
	// FetchError carries a non-fatal network error message when --fetch was
	// requested but the fetch itself failed. The rest of the struct still
	// reflects local state.
	FetchError string `json:"fetch_error,omitempty"`
}

// Status reads the current repo state.
func Status(configDir string) (StatusSummary, error) {
	out := StatusSummary{}
	ok, err := IsSyncRepo(configDir)
	if err != nil {
		return out, err
	}
	out.RepoExists = ok
	if !ok {
		return out, nil
	}
	out.Remote, _ = Remote(configDir)

	repo, err := git.PlainOpen(configDir)
	if err != nil {
		return out, err
	}
	if head, err := repo.Head(); err == nil {
		out.CurrentBranch = head.Name().Short()
	}

	w, err := repo.Worktree()
	if err != nil {
		return out, err
	}
	status, err := w.Status()
	if err != nil {
		return out, err
	}
	out.Dirty = !status.IsClean()
	for file := range status {
		out.ChangedFiles = append(out.ChangedFiles, file)
	}
	// Go map iteration is random; sort so repeated `sync status` calls
	// produce byte-identical output that agents can safely diff.
	sort.Strings(out.ChangedFiles)
	return out, nil
}

// StatusWithFetch returns the same summary as Status plus ahead/behind
// counts relative to origin/<branch>. The fetch is bounded by timeout;
// network failures populate FetchError but do not fail the whole call.
func StatusWithFetch(configDir string, timeout time.Duration) (StatusSummary, error) {
	out, err := Status(configDir)
	if err != nil || !out.RepoExists {
		return out, err
	}

	repo, err := git.PlainOpen(configDir)
	if err != nil {
		return out, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	fetchErr := repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       sshAuthFromEnv(),
	})
	if fetchErr != nil && fetchErr != git.NoErrAlreadyUpToDate {
		out.FetchError = fetchErr.Error()
		return out, nil
	}
	out.Fetched = true

	if out.CurrentBranch == "" {
		return out, nil
	}
	head, err := repo.Head()
	if err != nil {
		out.FetchError = err.Error()
		return out, nil
	}
	remoteRef, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", out.CurrentBranch), true)
	if err != nil {
		out.FetchError = err.Error()
		return out, nil
	}
	ahead, behind, err := countAheadBehind(repo, head.Hash(), remoteRef.Hash())
	if err != nil {
		out.FetchError = err.Error()
		return out, nil
	}
	out.Ahead = ahead
	out.Behind = behind
	return out, nil
}

// countAheadBehind walks commits reachable from local that are not
// reachable from remote (ahead) and vice versa (behind). Cheap for the
// tiny commit counts we expect in a profile-sync repo.
func countAheadBehind(repo *git.Repository, local, remote plumbing.Hash) (ahead, behind int, err error) {
	ahead, err = countExcluding(repo, local, remote)
	if err != nil {
		return 0, 0, err
	}
	behind, err = countExcluding(repo, remote, local)
	return ahead, behind, err
}

func countExcluding(repo *git.Repository, head, exclude plumbing.Hash) (int, error) {
	// Build the set of ancestors of exclude so we can prune them while
	// walking head. For small histories (profile repos), the full ancestor
	// set fits comfortably in memory.
	seen := map[plumbing.Hash]struct{}{}
	if err := walkAncestors(repo, exclude, seen); err != nil {
		return 0, err
	}
	iter, err := repo.Log(&git.LogOptions{From: head})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	n := 0
	for {
		c, err := iter.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// Don't treat pack-object corruption or any other iter error
			// as end-of-log. Surface so StatusWithFetch can report it in
			// StatusSummary.FetchError rather than returning a
			// silently-wrong count.
			return 0, err
		}
		if _, stop := seen[c.Hash]; stop {
			break
		}
		n++
	}
	return n, nil
}

func walkAncestors(repo *git.Repository, start plumbing.Hash, out map[plumbing.Hash]struct{}) error {
	iter, err := repo.Log(&git.LogOptions{From: start})
	if err != nil {
		return err
	}
	defer iter.Close()
	for {
		c, err := iter.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		out[c.Hash] = struct{}{}
	}
}

// Sentinel errors surfaced to the CLI layer for exit-code mapping.
var (
	// ErrRemoteEmpty is returned by CloneOrOpen when the remote has no
	// commits yet. The caller should fall through to InitRepo + SetRemote:
	// this is the expected state the first time a user runs
	// `sync setup --url <new repo>`.
	ErrRemoteEmpty = errors.New("remote repository is empty")

	// ErrDirtyWorkingTree is returned by Pull when the working tree has
	// uncommitted changes and --force was not set.
	ErrDirtyWorkingTree = errors.New("working tree has uncommitted changes; commit them (ccp sync push), or re-run with --force to discard")

	// ErrRemoteUnreachable wraps network-origin errors from go-git so
	// callers can map to a network-class exit code without matching on
	// error strings.
	ErrRemoteUnreachable = errors.New("remote unreachable")
)

// CloneOrOpen clones remote to configDir if it doesn't exist, otherwise opens.
// The clone path is used by `sync setup --url <remote>` when the local
// config dir is empty. Returns ErrRemoteEmpty if the remote has no commits.
func CloneOrOpen(configDir, remoteURL string) error {
	if exists, _ := IsSyncRepo(configDir); exists {
		return nil
	}
	// Clone into a temp sibling and then move ".git" into configDir —
	// preserves any existing files in configDir (the ones gitignored
	// remain, the synced ones overwrite).
	tmp, err := os.MkdirTemp(filepath.Dir(configDir), "ccp-clone-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	ctx, cancel := networkCtx()
	defer cancel()
	_, err = git.PlainCloneContext(ctx, tmp, false, &git.CloneOptions{
		URL:  remoteURL,
		Auth: sshAuthFromEnv(),
	})
	if err != nil {
		// Differentiate "remote empty" from other clone failures. Prefer
		// errors.Is on the known go-git sentinel — that's the contract.
		// The string-contains fallback catches very old or forked go-git
		// builds where the sentinel isn't returned.
		if errors.Is(err, transport.ErrEmptyRemoteRepository) {
			return ErrRemoteEmpty
		}
		if strings.Contains(err.Error(), "remote repository is empty") {
			return ErrRemoteEmpty
		}
		return fmt.Errorf("clone: %w", wrapNetwork(err))
	}

	// Move cloned .git into configDir.
	if err := os.Rename(filepath.Join(tmp, ".git"), filepath.Join(configDir, ".git")); err != nil {
		return fmt.Errorf("move .git: %w", err)
	}
	// Merge-copy cloned tracked content into configDir. Paths in tmp are
	// exactly the tracked files, so gitignored local state (manifest.toml,
	// backups/, lock, secrets/) is safe — it never appears in tmp to
	// overwrite anything.
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return err
	}
	for _, e := range entries {
		src := filepath.Join(tmp, e.Name())
		dst := filepath.Join(configDir, e.Name())
		if e.IsDir() {
			if err := mergeCopyDir(src, dst); err != nil {
				return err
			}
		} else {
			b, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dst, b, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// mergeCopyDir is copyDir without the "target parent must not exist"
// assumption — it walks src and writes each entry into dst, merging with
// anything already there. Files with matching paths are overwritten.
// Symlinks whose target escapes the source tree are refused.
func mergeCopyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if !symlinkWithin(path, link, src) {
				return fmt.Errorf("refusing to copy symlink %q → %q: target escapes source tree", path, link)
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			return streamCopyFile(path, target, info.Mode().Perm())
		}
	})
}

// streamCopyFile copies src → dst streaming via io.Copy so large files don't
// load entirely into memory. mode sets the destination file's permission.
func streamCopyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// signature builds a git author signature from env vars, falling back to
// sensible ccp defaults. Users who already have GIT_AUTHOR_NAME etc in env
// get those.
func signature() *object.Signature {
	name := strings.TrimSpace(os.Getenv("GIT_AUTHOR_NAME"))
	if name == "" {
		name = "ccp"
	}
	email := strings.TrimSpace(os.Getenv("GIT_AUTHOR_EMAIL"))
	if email == "" {
		host, _ := os.Hostname()
		email = "ccp@" + host
	}
	return &object.Signature{Name: name, Email: email, When: time.Now()}
}
