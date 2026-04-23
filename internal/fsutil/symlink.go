// Package fsutil holds small filesystem helpers shared across ccp's
// internal packages. Members here carry cross-cutting invariants — most
// importantly, SymlinkWithin backs the security boundary that rejects
// symlinks whose targets escape a profile's source tree.
package fsutil

import (
	"path/filepath"
	"strings"
)

// SymlinkWithin reports whether a symlink at linkPath pointing at linkTarget
// resolves to a path strictly inside srcRoot. Accepts either absolute or
// relative linkTarget values. Self-referential links (target == srcRoot)
// are rejected so a symlink like settings.json -> . cannot create a
// filesystem loop during a recursive copy.
func SymlinkWithin(linkPath, linkTarget, srcRoot string) bool {
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
	if rel == "." || rel == "" {
		return false
	}
	return !strings.HasPrefix(rel, "..")
}
