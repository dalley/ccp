package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/dalley/ccp/internal/paths"
	"github.com/dalley/ccp/internal/refs"
)

// RuntimeManifestSchemaVersion is the schema version written into the
// per-profile runtime manifest file. Increment when the on-disk shape
// changes in a way that old readers can't understand.
const RuntimeManifestSchemaVersion = 1

// KeychainLookup is a package-level hook that the CLI layer wires to
// secret.Get. Accepting only (profile, key) avoids pulling the secret
// package into the profile package (secret already imports profile, so
// a direct call would cycle). Returns ErrSecretRefUnresolved-wrapped
// errors when the key can't be found; tests may override to control
// resolution.
//
// Nil means "no keychain support wired" — callers that receive a
// keychain ref will see an ErrSecretRefUnresolved error.
var KeychainLookup func(profile, key string) (string, error)

// runtimeManifest is the per-profile JSON state tracking which files in
// the runtime dir were rendered by ccp (vs symlinked or created by
// Claude). Paths are relative to ConfigDir, stored with forward slashes
// on every platform so a manifest written on one OS can be read on
// another in the unlikely-but-possible case of a shared runtime dir.
type runtimeManifest struct {
	SchemaVersion int      `json:"schema_version"`
	Files         []string `json:"files"`
}

// Paths is intentionally tiny so equality / mutation is cheap. The
// schema comment above is load-bearing for future migrations.

// runtimeManifestLoad returns the manifest for profile `name`. Missing
// file → empty manifest. Malformed JSON is an error (we won't silently
// drop tracking state and risk treating ccp-rendered files as Claude-
// written on the next Build).
func runtimeManifestLoad(p paths.Paths, name string) (runtimeManifest, error) {
	path := p.RuntimeManifestPath(name)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return runtimeManifest{SchemaVersion: RuntimeManifestSchemaVersion}, nil
		}
		return runtimeManifest{}, fmt.Errorf("read %s: %w", path, err)
	}
	if len(b) == 0 {
		return runtimeManifest{SchemaVersion: RuntimeManifestSchemaVersion}, nil
	}
	var m runtimeManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return runtimeManifest{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if m.SchemaVersion == 0 {
		m.SchemaVersion = RuntimeManifestSchemaVersion
	}
	return m, nil
}

// runtimeManifestSave writes the manifest atomically (temp-in-same-dir +
// rename(2)). Mirrors manifest.Save's discipline. Empty file list
// removes the manifest so "no rendered files" is a file-not-present
// invariant that's easy to assert against in tests.
func runtimeManifestSave(p paths.Paths, name string, m runtimeManifest) error {
	if m.SchemaVersion == 0 {
		m.SchemaVersion = RuntimeManifestSchemaVersion
	}
	// Canonicalize: dedupe, sort. Stable on-disk output regardless of
	// caller mutation order.
	seen := map[string]struct{}{}
	files := make([]string, 0, len(m.Files))
	for _, f := range m.Files {
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		files = append(files, f)
	}
	sort.Strings(files)
	m.Files = files

	path := p.RuntimeManifestPath(name)
	if len(m.Files) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create runtime-manifest dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rtm-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", filepath.Dir(path), err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}

// hasEntry reports whether relPath is tracked in m. relPath must use
// forward slashes (the on-disk encoding).
func (m runtimeManifest) hasEntry(relPath string) bool {
	for _, f := range m.Files {
		if f == relPath {
			return true
		}
	}
	return false
}

// addEntry appends relPath if not already present.
func (m *runtimeManifest) addEntry(relPath string) {
	if m.hasEntry(relPath) {
		return
	}
	m.Files = append(m.Files, relPath)
}

// removeEntry drops relPath. No-op if absent.
func (m *runtimeManifest) removeEntry(relPath string) {
	for i, f := range m.Files {
		if f == relPath {
			m.Files = append(m.Files[:i], m.Files[i+1:]...)
			return
		}
	}
}

// toRuntimeRel returns the forward-slashed path relative to configDir
// for storage in the manifest. Caller guarantees abs is inside configDir.
func toRuntimeRel(configDir, abs string) string {
	rel, err := filepath.Rel(configDir, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

// renderFile reads src, runs refs.Render through resolver, and
// atomically writes to dst with mode (srcMode & 0100) | 0600. Writes a
// sibling temp file + rename(2) so a failure leaves prior content
// intact. If dst is a symlink it is removed first (ensureSymlink-style
// takeover). Refuses to overwrite a regular file at dst unless the
// caller has recorded it in `known` — that's the "don't clobber
// Claude-written content" invariant.
//
// The rendered-file mode computation is `(srcMode & 0100) | 0600`:
//   - preserves the user-execute bit from source (hook scripts stay
//     executable);
//   - always sets user read+write;
//   - strips group/other read (rendered content may contain secrets).
//
// Returns a wrapped refs.ErrSecretRefUnresolved when resolution fails.
func renderFile(ctx context.Context, src, dst string, srcMode os.FileMode, resolver refs.Resolver, known map[string]struct{}) error {
	// Per-file TOCTOU defence: rejectEscapingSymlinksInSource runs before
	// the render pass, but an attacker with write access could swap a
	// non-escaping symlink for an escaping one between the walk and this
	// read. O_NOFOLLOW refuses the symlink outright — see
	// readFileNoFollow's docstring.
	b, err := readFileNoFollow(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	out, err := refs.Render(ctx, b, resolver)
	if err != nil {
		return fmt.Errorf("render %s: %w", src, err)
	}

	// Rendering-identity invariant: refuse to overwrite a regular file
	// at dst that ccp hasn't rendered before. Symlinks are always safe
	// to replace (we put them there). A regular file we created
	// previously is in `known` (or we're about to add it).
	if info, err := os.Lstat(dst); err == nil {
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if rmErr := os.Remove(dst); rmErr != nil {
				return fmt.Errorf("remove stale symlink %s: %w", dst, rmErr)
			}
		case info.Mode().IsRegular():
			if _, ok := known[dst]; !ok {
				return fmt.Errorf("%s exists and is not a ccp-rendered file; refusing to overwrite", dst)
			}
		default:
			return fmt.Errorf("%s exists and is not a regular file or symlink; refusing to overwrite", dst)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	mode := (srcMode & 0o100) | 0o600

	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".render-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", filepath.Dir(dst), err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}

// defaultResolver constructs the resolver used by BuildSymlinksCtx /
// RefreshSymlinksCtx. Wrapped in a variable so tests can point it at a
// deterministic fake without touching env.
var defaultResolver = func(profileName string) refs.Resolver {
	return refs.DefaultResolver{
		Profile:    profileName,
		KeyringGet: keychainGetShim,
		EnvLookup:  os.LookupEnv,
	}
}

// keychainGetShim adapts the profile-package KeychainLookup hook to the
// refs.DefaultResolver.KeyringGet signature. `service` is always "ccp"
// by construction upstream; we ignore it here (our single-service model
// is encoded in secret.Get).
func keychainGetShim(service, account, key string) (string, error) {
	if KeychainLookup == nil {
		return "", fmt.Errorf("refs: keychain lookup not wired (ccp must init secret package): %w",
			refs.ErrSecretRefUnresolved)
	}
	return KeychainLookup(account, key)
}

// -----------------------------------------------------------------------------
// Rendering helpers used by BuildSymlinks / RefreshSymlinks
// -----------------------------------------------------------------------------

// pathsForProfile recovers the paths.Paths view for a profile by
// inverting the ProfileSourceDir / ProfileConfigDir conventions. The
// runtime-manifest path depends on ConfigDir; rather than thread Paths
// through every call site, we reconstruct it from what Profile knows.
func pathsForProfile(pr Profile) paths.Paths {
	// pr.SourceDir is <configRoot>/profiles/<name>; two parents up is
	// the ccp config root.
	configRoot := filepath.Dir(filepath.Dir(pr.SourceDir))
	// pr.ConfigDir is <home>/.claude-<name>; one parent up is home.
	home := filepath.Dir(pr.ConfigDir)
	return paths.Paths{
		Home:               home,
		ConfigDir:          configRoot,
		ProfilesDir:        filepath.Join(configRoot, "profiles"),
		BackupsDir:         filepath.Join(configRoot, "backups"),
		SecretsDir:         filepath.Join(configRoot, "secrets"),
		RuntimeManifestDir: filepath.Join(configRoot, "runtime-manifest"),
		ManifestPath:       filepath.Join(configRoot, "manifest.toml"),
		AllowlistPath:      filepath.Join(configRoot, "allowlist.toml"),
		LockPath:           filepath.Join(configRoot, "lock"),
		ClaudeHome:         filepath.Join(home, ".claude"),
	}
}

// dirHasRefs walks dir and returns true if any regular file under it
// contains a recognized ref scheme. Short-circuits on first match (via
// HasAnyRefs). Missing dir → (false, nil).
func dirHasRefs(dir string) (bool, error) {
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return refs.HasAnyRefs(dir)
}

// renderSessionMu guards runtime-manifest load/save so a BuildSymlinks
// called concurrently with itself (within the same process) doesn't
// race on manifest state. Cross-process safety is the caller's job (the
// CLI takes withLock before invoking).
var renderSessionMu sync.Mutex

// knownPaths returns the set of absolute runtime-dir paths currently
// tracked in rtm as ccp-rendered. Used by renderFile to decide whether
// replacing an existing regular file is safe.
func knownPaths(configDir string, rtm runtimeManifest) map[string]struct{} {
	out := make(map[string]struct{}, len(rtm.Files))
	for _, rel := range rtm.Files {
		out[filepath.Join(configDir, filepath.FromSlash(rel))] = struct{}{}
	}
	return out
}

// -----------------------------------------------------------------------------
// Directory transitions
// -----------------------------------------------------------------------------

// removeCcpOwnedEntries walks runtimeDir (a real dir that used to hold
// ccp symlinks + rendered files) and removes only entries that ccp
// owns: symlinks pointing into sourceDir, and regular files listed in
// rtm (ccp-rendered). Leaves foreign files/symlinks intact so a user
// who dropped a debug note in the runtime dir doesn't lose it.
//
// rtmPaths is a set of absolute runtime paths currently tracked. Any
// entry we successfully remove is also pruned from rtm via removeEntry
// on the caller side — this function only reports what it removed via
// the returned list.
//
// After removal, if the dir is empty it is also removed (so the caller
// can replace it with a top-level symlink). If the dir still has
// foreign content, we leave it and return it as "not empty" via the
// bool — the caller surfaces an error.
func removeCcpOwnedEntries(runtimeDir, sourceDir string, rtmPaths map[string]struct{}) (removed []string, leftNonEmpty bool, err error) {
	if _, statErr := os.Stat(runtimeDir); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, false, nil
		}
		return nil, false, statErr
	}

	walkErr := filepath.Walk(runtimeDir, func(path string, info fs.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		if path == runtimeDir {
			return nil
		}
		// Symlinks: remove if they point into sourceDir.
		if info.Mode()&os.ModeSymlink != 0 {
			target, rerr := os.Readlink(path)
			if rerr != nil {
				return nil // leave it; not fatal
			}
			abs := target
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(filepath.Dir(path), abs)
			}
			rel, rerr := filepath.Rel(sourceDir, abs)
			if rerr != nil || strings.HasPrefix(rel, "..") || rel == "." {
				return nil // foreign symlink; skip
			}
			if rmErr := os.Remove(path); rmErr != nil {
				return rmErr
			}
			removed = append(removed, path)
			return nil
		}
		// Regular files: remove only if tracked in the manifest.
		if info.Mode().IsRegular() {
			if _, ok := rtmPaths[path]; ok {
				if rmErr := os.Remove(path); rmErr != nil {
					return rmErr
				}
				removed = append(removed, path)
			}
			return nil
		}
		// Dirs: skip; we'll post-process them below.
		return nil
	})
	if walkErr != nil {
		return removed, false, walkErr
	}

	// Second pass: remove empty subdirectories bottom-up, then the top
	// dir itself if it's empty. If anything remains at the top, the
	// caller can't safely replace it with a symlink.
	_ = filepath.Walk(runtimeDir, func(path string, info fs.FileInfo, werr error) error {
		if werr != nil || !info.IsDir() || path == runtimeDir {
			return nil
		}
		entries, _ := os.ReadDir(path)
		if len(entries) == 0 {
			_ = os.Remove(path)
		}
		return nil
	})

	entries, _ := os.ReadDir(runtimeDir)
	if len(entries) == 0 {
		if rmErr := os.Remove(runtimeDir); rmErr != nil {
			return removed, false, rmErr
		}
		return removed, false, nil
	}
	return removed, true, nil
}

// -----------------------------------------------------------------------------
// Recursive build for a ref-bearing directory
// -----------------------------------------------------------------------------

// buildRefDir mirrors the source subtree at srcDir into runtimeDir,
// symlinking files/dirs without refs and rendering files with refs.
// Recurses into subdirectories, materializing real 0700 dirs along the
// way. Appends rendered relative paths to rtm.Files.
//
// Errors are collected and returned as a slice so a single failing
// file doesn't abort the whole build — callers join them via
// errors.Join for the final return.
func buildRefDir(ctx context.Context, srcDir, runtimeDir, configDir string, resolver refs.Resolver, rtm *runtimeManifest) []error {
	var errs []error

	// Ensure the runtime dir is a real 0700 dir (caller may have
	// already removed a stale symlink).
	if info, err := os.Lstat(runtimeDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			if rmErr := os.Remove(runtimeDir); rmErr != nil {
				errs = append(errs, fmt.Errorf("remove stale symlink %s: %w", runtimeDir, rmErr))
				return errs
			}
		}
	}
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		errs = append(errs, fmt.Errorf("mkdir %s: %w", runtimeDir, err))
		return errs
	}
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		// Chmod failure on an existing dir is a warning-level issue; we
		// still proceed so content renders. Record but don't abort.
		errs = append(errs, fmt.Errorf("chmod 0700 %s: %w", runtimeDir, err))
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		errs = append(errs, fmt.Errorf("read dir %s: %w", srcDir, err))
		return errs
	}

	known := knownPaths(configDir, *rtm)

	for _, e := range entries {
		name := e.Name()
		srcPath := filepath.Join(srcDir, name)
		dstPath := filepath.Join(runtimeDir, name)

		info, err := os.Lstat(srcPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("lstat %s: %w", srcPath, err))
			continue
		}

		switch {
		case info.Mode()&os.ModeSymlink != 0:
			// Intra-tree symlinks are already validated by
			// rejectEscapingSymlinksInSource; reproduce as a runtime
			// symlink pointing at the source symlink (the OS will
			// follow it transparently).
			if lerr := ensureSymlink(srcPath, dstPath); lerr != nil {
				errs = append(errs, lerr)
			}

		case info.IsDir():
			// We're already inside a buildRefDir recursion — the outer
			// buildItemDir confirmed refs exist somewhere in this tree,
			// so a second dirHasRefs pass on every subdir would scan
			// each leaf twice (O(N*M) for an N-wide tree of M-deep
			// files). Instead we always materialize a real dir and
			// recurse; the per-file refs.HasRefs check below decides
			// render-vs-symlink at the leaf. The cost of symlinking a
			// ref-free subtree leaf-by-leaf instead of wholesale is
			// trivial (one readdir + N symlinks), while the saving on
			// the ref-bearing case scales with tree depth.
			subErrs := buildRefDir(ctx, srcPath, dstPath, configDir, resolver, rtm)
			errs = append(errs, subErrs...)

		default:
			// Regular file. Use O_NOFOLLOW per the same TOCTOU argument as
			// renderFile: the initial escape-check walk is not atomic
			// with this read, so a concurrent swap from a non-escaping
			// symlink to an escaping one would otherwise slip through.
			b, rerr := readFileNoFollow(srcPath)
			if rerr != nil {
				errs = append(errs, fmt.Errorf("read %s: %w", srcPath, rerr))
				continue
			}
			if !refs.HasRefs(b) {
				if lerr := ensureSymlink(srcPath, dstPath); lerr != nil {
					errs = append(errs, lerr)
				}
				continue
			}
			// Ref-bearing file: render.
			if rerr := renderFile(ctx, srcPath, dstPath, info.Mode().Perm(), resolver, known); rerr != nil {
				errs = append(errs, rerr)
				continue
			}
			// Track in the manifest under the forward-slashed relative path.
			rel := toRuntimeRel(configDir, dstPath)
			rtm.addEntry(rel)
			known[dstPath] = struct{}{}
		}
	}
	return errs
}

// -----------------------------------------------------------------------------
// Pruning
// -----------------------------------------------------------------------------

// pruneStaleRenders drops entries from rtm whose source no longer has
// refs (or no longer exists). Corresponding runtime files are removed
// so BuildSymlinks can re-symlink them next pass.
//
// Only called from RefreshSymlinks — BuildSymlinks is additive.
func pruneStaleRenders(pr Profile, rtm *runtimeManifest) []error {
	var errs []error
	keep := rtm.Files[:0]
	for _, rel := range rtm.Files {
		dst := filepath.Join(pr.ConfigDir, filepath.FromSlash(rel))
		src := filepath.Join(pr.SourceDir, filepath.FromSlash(rel))
		// If source is gone or has no refs anymore, drop the rendered
		// file so a non-ref source can be re-symlinked. O_NOFOLLOW for
		// the same TOCTOU reason as renderFile / buildRefDir — a swapped
		// symlink here would let us mistakenly KEEP a rendered file
		// whose source now points outside the tree.
		b, err := readFileNoFollow(src)
		if err != nil || !refs.HasRefs(b) {
			if rmErr := os.Remove(dst); rmErr != nil && !os.IsNotExist(rmErr) {
				errs = append(errs, fmt.Errorf("remove stale rendered %s: %w", dst, rmErr))
			}
			continue
		}
		keep = append(keep, rel)
	}
	rtm.Files = keep
	return errs
}

// -----------------------------------------------------------------------------
// Context convenience
// -----------------------------------------------------------------------------

// ContextOrBackground returns ctx, or context.Background() when ctx is
// nil. Lets callers pass nil explicitly without triggering a panic on
// nil-receiver method calls inside refs.Render.
func ContextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
