package profile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dalley/ccp/internal/fsutil"
	"github.com/dalley/ccp/internal/refs"
)

// BuildSymlinks materializes the profile's runtime config directory by
// symlinking each SharedItems entry from the source dir into ConfigDir —
// or rendering it when the source contains `{{ ... }}` secret references.
//
// Only items that exist in the source dir are linked. Existing links in
// ConfigDir that point to the correct target are left alone (idempotent).
// Existing files/links at the target path that DON'T match are returned as
// an error — we never silently overwrite user content.
//
// Per-item render failures are accumulated via errors.Join and returned
// after processing all items — successful renders persist even when
// others fail. This matches Key Decision 8 in the v2.0 plan: a single
// unresolved keychain ref on file A must not block the render of file B.
// The joined error unwraps to refs.ErrSecretRefUnresolved via errors.Is
// for callers that want to distinguish "unresolved refs" from other
// classes of failure.
//
// BuildSymlinks delegates to BuildSymlinksCtx(context.Background()).
func (pr Profile) BuildSymlinks() error {
	return pr.BuildSymlinksCtx(context.Background())
}

// BuildSymlinksCtx is BuildSymlinks with an explicit ctx forwarded to
// refs.Render. Use this when the caller has a timeout for ref resolution
// (ccp exec's per-invocation budget).
func (pr Profile) BuildSymlinksCtx(ctx context.Context) error {
	ctx = ContextOrBackground(ctx)
	if err := rejectEscapingSymlinksInSource(pr.SourceDir); err != nil {
		return err
	}
	if err := os.MkdirAll(pr.ConfigDir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	p := pathsForProfile(pr)
	resolver := defaultResolver(pr.Name)

	// Serialize with any other in-process BuildSymlinks so we don't race
	// on the runtime manifest. The CLI's withLock handles cross-process.
	renderSessionMu.Lock()
	defer renderSessionMu.Unlock()

	rtm, err := runtimeManifestLoad(p, pr.Name)
	if err != nil {
		return fmt.Errorf("load runtime manifest: %w", err)
	}

	var errs []error
	for _, item := range SharedItems {
		src := filepath.Join(pr.SourceDir, item.Name)
		info, err := os.Lstat(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue // source doesn't have this item; that's fine.
			}
			errs = append(errs, fmt.Errorf("stat source %s: %w", src, err))
			continue
		}
		// A top-level SharedItem that's itself a symlink would point the
		// runtime symlink at another symlink — legal but confusing. We
		// require SharedItems entries to be regular files / directories
		// directly. Intra-tree symlinks UNDER a SharedItem directory
		// (e.g. hooks/shared.sh -> base.sh) are fine — the walk above
		// already verified they stay in-tree.
		if info.Mode()&os.ModeSymlink != 0 {
			errs = append(errs, fmt.Errorf("refusing to link %s: top-level SharedItem is itself a symlink", src))
			continue
		}
		dst := filepath.Join(pr.ConfigDir, item.Name)
		if item.Dir {
			if ierr := buildItemDir(ctx, src, dst, pr, &rtm, resolver); ierr != nil {
				errs = append(errs, ierr)
			}
			continue
		}
		// File item: dispatch on ref-presence.
		b, rerr := os.ReadFile(src)
		if rerr != nil {
			errs = append(errs, fmt.Errorf("read %s: %w", src, rerr))
			continue
		}
		if refs.HasRefs(b) {
			known := knownPaths(pr.ConfigDir, rtm)
			if err := renderFile(ctx, src, dst, info.Mode().Perm(), resolver, known); err != nil {
				errs = append(errs, err)
				continue
			}
			rtm.addEntry(toRuntimeRel(pr.ConfigDir, dst))
		} else {
			// Non-ref file: legacy symlink path. If a rendered file
			// from a prior pass sits at dst, drop it + untrack so the
			// symlink can take over cleanly.
			rel := toRuntimeRel(pr.ConfigDir, dst)
			if rtm.hasEntry(rel) {
				_ = os.Remove(dst)
				rtm.removeEntry(rel)
			}
			if err := ensureSymlink(src, dst); err != nil {
				errs = append(errs, err)
			}
		}
	}

	// Persist the manifest regardless of per-item errors — entries we
	// successfully added must be tracked so a subsequent call knows
	// not to treat them as Claude-written.
	if err := runtimeManifestSave(p, pr.Name, rtm); err != nil {
		errs = append(errs, fmt.Errorf("save runtime manifest: %w", err))
	}

	return errors.Join(errs...)
}

// buildItemDir handles one top-level directory SharedItem. Dispatches
// on whether the source subtree contains refs anywhere. Handles the
// runtime mode transitions (symlink <-> real dir) described in the
// plan. Mutates rtm to reflect any rendered-file additions.
func buildItemDir(ctx context.Context, src, dst string, pr Profile, rtm *runtimeManifest, resolver refs.Resolver) error {
	hasRefs, err := dirHasRefs(src)
	if err != nil {
		return fmt.Errorf("scan %s for refs: %w", src, err)
	}

	if !hasRefs {
		// No refs anywhere: we want a top-level symlink. Handle the
		// real-dir → symlink transition if a prior state left a real
		// dir behind.
		info, lerr := os.Lstat(dst)
		if lerr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				// Already a symlink; ensureSymlink handles retargeting.
				return ensureSymlink(src, dst)
			}
			if info.IsDir() {
				// Prune ccp-owned entries; if the dir ends up empty
				// (or fully removed) we replace it with a symlink.
				rtmPaths := knownPaths(pr.ConfigDir, *rtm)
				_, leftover, rmErr := removeCcpOwnedEntries(dst, pr.SourceDir, rtmPaths)
				if rmErr != nil {
					return fmt.Errorf("clean old rendered dir %s: %w", dst, rmErr)
				}
				// Any manifest entry under this subtree is now stale.
				prefix := toRuntimeRel(pr.ConfigDir, dst) + "/"
				keep := rtm.Files[:0]
				for _, f := range rtm.Files {
					if f == toRuntimeRel(pr.ConfigDir, dst) || (len(f) >= len(prefix) && f[:len(prefix)] == prefix) {
						continue
					}
					keep = append(keep, f)
				}
				rtm.Files = keep
				if leftover {
					return fmt.Errorf("runtime dir %s has foreign content; refusing to replace with symlink", dst)
				}
			}
		} else if !os.IsNotExist(lerr) {
			return lerr
		}
		return ensureSymlink(src, dst)
	}

	// Refs somewhere: the dir must be real. Handle the symlink → real
	// dir transition and recurse.
	if info, lerr := os.Lstat(dst); lerr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			if rmErr := os.Remove(dst); rmErr != nil {
				return fmt.Errorf("remove symlink %s: %w", dst, rmErr)
			}
		}
	}
	if subErrs := buildRefDir(ctx, src, dst, pr.ConfigDir, resolver, rtm); len(subErrs) > 0 {
		return errors.Join(subErrs...)
	}
	return nil
}

// rejectEscapingSymlinksInSource walks root and rejects only symlinks whose
// resolved target escapes root. Intra-tree relative symlinks (e.g.
// hooks/post-tool -> ../shared.sh where both sides stay inside the profile)
// are allowed; this matches what copyTree preserves during seeding.
//
// The profile source is meant to hold only regular files, directories, and
// optionally intra-tree symlinks — a symlink pointing outside the source
// tree is an exfiltration vector (a malicious git commit can place e.g.
// hooks/post-tool-use -> ../../../../.aws/credentials, which BuildSymlinks
// would otherwise expose to Claude via the runtime dir).
func rejectEscapingSymlinksInSource(root string) error {
	if _, err := os.Lstat(root); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if d.Type()&os.ModeSymlink == 0 {
			return nil
		}
		link, lerr := os.Readlink(path)
		if lerr != nil {
			return lerr
		}
		if !fsutil.SymlinkWithin(path, link, root) {
			rel, _ := filepath.Rel(root, path)
			return fmt.Errorf("refusing to build symlinks: profile source symlink %s → %s escapes the profile tree (suspected tampering)", rel, link)
		}
		return nil
	})
}

// RefreshSymlinks does what BuildSymlinks does AND additionally:
//   - removes any symlink in ConfigDir that points into SourceDir but whose
//     target no longer exists (fixes stale links);
//   - prunes runtime-manifest entries whose source is gone or no longer has
//     refs, removing their runtime content so the next Build symlinks them
//     plainly.
//
// Delegates to RefreshSymlinksCtx(context.Background()).
func (pr Profile) RefreshSymlinks() error {
	return pr.RefreshSymlinksCtx(context.Background())
}

// RefreshSymlinksCtx is RefreshSymlinks with an explicit context.
func (pr Profile) RefreshSymlinksCtx(ctx context.Context) error {
	ctx = ContextOrBackground(ctx)
	p := pathsForProfile(pr)

	// Prune stale rendered entries BEFORE the build so the build can
	// re-symlink any source that lost its refs. We hold the in-process
	// manifest lock across prune + build to avoid inconsistent state.
	renderSessionMu.Lock()
	rtm, err := runtimeManifestLoad(p, pr.Name)
	if err != nil {
		renderSessionMu.Unlock()
		return fmt.Errorf("load runtime manifest: %w", err)
	}
	pruneErrs := pruneStaleRenders(pr, &rtm)
	if err := runtimeManifestSave(p, pr.Name, rtm); err != nil {
		renderSessionMu.Unlock()
		return fmt.Errorf("save runtime manifest after prune: %w", err)
	}
	renderSessionMu.Unlock()

	if err := pr.BuildSymlinksCtx(ctx); err != nil {
		if len(pruneErrs) > 0 {
			return errors.Join(append([]error{err}, pruneErrs...)...)
		}
		return err
	}

	entries, err := os.ReadDir(pr.ConfigDir)
	if err != nil {
		if os.IsNotExist(err) {
			if len(pruneErrs) > 0 {
				return errors.Join(pruneErrs...)
			}
			return nil
		}
		if len(pruneErrs) > 0 {
			return errors.Join(append([]error{err}, pruneErrs...)...)
		}
		return err
	}
	for _, e := range entries {
		full := filepath.Join(pr.ConfigDir, e.Name())
		link, err := os.Readlink(full)
		if err != nil {
			continue // not a symlink
		}
		abs := link
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(filepath.Dir(full), abs)
		}
		// Only prune links that point into our own source tree — never touch
		// symlinks created by Claude or by the user.
		rel, err := filepath.Rel(pr.SourceDir, abs)
		if err != nil || rel == "." || startsWithDotDot(rel) {
			continue
		}
		if _, err := os.Stat(abs); errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(full)
		}
	}
	if len(pruneErrs) > 0 {
		return errors.Join(pruneErrs...)
	}
	return nil
}

// RemoveSymlinks deletes any symlinks in ConfigDir whose target is inside
// SourceDir. It does not touch regular files or unrelated symlinks —
// preserving Claude's runtime state (auth, session, cache files). It also
// removes rendered files tracked in the runtime manifest, then clears the
// manifest so the profile goes back to a pristine state.
func (pr Profile) RemoveSymlinks() error {
	// Drop rendered files first, then clear the manifest. We do this
	// under the in-process mutex so a concurrent BuildSymlinks doesn't
	// race with the cleanup.
	p := pathsForProfile(pr)
	renderSessionMu.Lock()
	rtm, rErr := runtimeManifestLoad(p, pr.Name)
	if rErr == nil {
		for _, rel := range rtm.Files {
			_ = os.Remove(filepath.Join(pr.ConfigDir, filepath.FromSlash(rel)))
		}
		rtm.Files = nil
		_ = runtimeManifestSave(p, pr.Name, rtm)
	}
	renderSessionMu.Unlock()

	entries, err := os.ReadDir(pr.ConfigDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		full := filepath.Join(pr.ConfigDir, e.Name())
		link, err := os.Readlink(full)
		if err != nil {
			continue
		}
		abs := link
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(filepath.Dir(full), abs)
		}
		rel, err := filepath.Rel(pr.SourceDir, abs)
		if err != nil || startsWithDotDot(rel) {
			continue
		}
		_ = os.Remove(full)
	}
	return nil
}

// ensureSymlink creates dst → src. If dst already exists as a matching
// symlink, it's a no-op. If dst exists but isn't that symlink, returns an
// error rather than overwriting.
func ensureSymlink(src, dst string) error {
	if info, err := os.Lstat(dst); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			existing, rerr := os.Readlink(dst)
			if rerr == nil && existing == src {
				return nil
			}
			// Different target: remove and re-link so a moved profile source
			// is picked up without manual intervention.
			if err := os.Remove(dst); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("%s already exists and is not a symlink; refusing to overwrite", dst)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.Symlink(src, dst)
}

func startsWithDotDot(rel string) bool {
	return len(rel) >= 2 && rel[:2] == ".."
}
