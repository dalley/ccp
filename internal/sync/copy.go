package sync

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// copyDir recursively copies src into dst. Symlinks are reproduced only
// when their resolved target stays inside src — escape attempts (e.g., a
// profile from a hostile remote linking settings.json → /etc/passwd) are
// refused with an error rather than materialized.
func copyDir(src, dst string) error {
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
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			defer in.Close()
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, in); err != nil {
				_ = out.Close()
				return err
			}
			return out.Close()
		}
	})
}

// symlinkWithin reports whether a symlink at linkPath pointing at linkTarget
// resolves inside srcRoot. Accepts absolute or relative linkTarget values.
func symlinkWithin(linkPath, linkTarget, srcRoot string) bool {
	abs := linkTarget
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(filepath.Dir(linkPath), abs)
	}
	abs = filepath.Clean(abs)
	absRoot, err := filepath.Abs(srcRoot)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, abs)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}
