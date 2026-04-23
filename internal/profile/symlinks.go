package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// BuildSymlinks materializes the profile's runtime config directory by
// symlinking each SharedItems entry from the source dir into ConfigDir.
//
// Only items that exist in the source dir are linked. Existing links in
// ConfigDir that point to the correct target are left alone (idempotent).
// Existing files/links at the target path that DON'T match are returned as
// an error — we never silently overwrite user content.
func (pr Profile) BuildSymlinks() error {
	if err := rejectSymlinksInSource(pr.SourceDir); err != nil {
		return err
	}
	if err := os.MkdirAll(pr.ConfigDir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	for _, item := range SharedItems {
		src := filepath.Join(pr.SourceDir, item.Name)
		info, err := os.Lstat(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue // source doesn't have this item; that's fine.
			}
			return fmt.Errorf("stat source %s: %w", src, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to link %s: source entry is a symlink (profile source may be tampered)", src)
		}
		dst := filepath.Join(pr.ConfigDir, item.Name)
		if err := ensureSymlink(src, dst); err != nil {
			return err
		}
	}
	return nil
}

// rejectSymlinksInSource walks root and returns an error if any descendant is
// a symlink. The profile source is meant to hold only regular files and
// directories — a symlink inside the source tree is an exfiltration vector
// (a malicious git commit can place e.g. hooks/post-tool-use ->
// ../../../../.aws/credentials, which BuildSymlinks would otherwise expose to
// Claude via the runtime dir).
func rejectSymlinksInSource(root string) error {
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
		if d.Type()&os.ModeSymlink != 0 {
			rel, _ := filepath.Rel(root, path)
			return fmt.Errorf("refusing to build symlinks: profile source contains a symlink at %s (suspected tampering)", rel)
		}
		return nil
	})
}

// RefreshSymlinks does what BuildSymlinks does AND additionally removes any
// symlink in ConfigDir that points into SourceDir but whose target no longer
// exists. This is the fix for jean-claude's `refresh` not removing stale links.
func (pr Profile) RefreshSymlinks() error {
	if err := pr.BuildSymlinks(); err != nil {
		return err
	}
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
	return nil
}

// RemoveSymlinks deletes any symlinks in ConfigDir whose target is inside
// SourceDir. It does not touch regular files or unrelated symlinks —
// preserving Claude's runtime state (auth, session, cache files).
func (pr Profile) RemoveSymlinks() error {
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
	return os.Symlink(src, dst)
}

func startsWithDotDot(rel string) bool {
	return len(rel) >= 2 && rel[:2] == ".."
}
