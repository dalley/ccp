// Package profile manages named Claude Code profiles: their source tree
// (git-syncable) and runtime tree (what CLAUDE_CONFIG_DIR points at).
package profile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dalley/ccp/internal/paths"
)

// nameRe matches a valid profile name. Lowercase keeps filesystem behavior
// consistent across case-insensitive macOS and case-sensitive Linux; hyphens
// and underscores cover real-world needs without requiring quoting.
var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// ValidateName returns nil if name is a legal profile name.
func ValidateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid profile name %q: must start with a lowercase letter, contain only [a-z0-9_-], and be 1-63 characters long", name)
	}
	return nil
}

// Profile describes one profile's locations on disk.
type Profile struct {
	Name      string
	SourceDir string // ~/.config/ccp/profiles/<name>
	ConfigDir string // ~/.claude-<name>
}

// New constructs a Profile value from Paths and a name. It does not touch
// the filesystem. Callers that take the name from untrusted input (manifest,
// CLI args) must call ValidateName first or use NewChecked.
func New(p paths.Paths, name string) Profile {
	return Profile{
		Name:      name,
		SourceDir: p.ProfileSourceDir(name),
		ConfigDir: p.ProfileConfigDir(name),
	}
}

// NewChecked is New with ValidateName applied first. Use this on any path
// where the name crosses a trust boundary (CLI arguments, manifest fields,
// sync-pulled marker files).
func NewChecked(p paths.Paths, name string) (Profile, error) {
	if err := ValidateName(name); err != nil {
		return Profile{}, err
	}
	return New(p, name), nil
}

// Exists reports whether the profile's source directory is present.
func (pr Profile) Exists() bool {
	info, err := os.Stat(pr.SourceDir)
	return err == nil && info.IsDir()
}

// List enumerates profiles by reading ~/.config/ccp/profiles. Returns names
// sorted for stable output.
func List(p paths.Paths) ([]Profile, error) {
	entries, err := os.ReadDir(p.ProfilesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read profiles dir: %w", err)
	}
	var out []Profile
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if ValidateName(e.Name()) != nil {
			continue
		}
		out = append(out, New(p, e.Name()))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// copyTree recursively copies src into dst. Regular files, directories, and
// symlinks are handled. Modes are preserved. Permission errors abort.
//
// Symlinks are only reproduced when their resolved target stays inside src.
// A profile cloned from a hostile git remote could otherwise carry a link
// like settings.json -> /etc/passwd that ccp would silently materialize in
// the user's config.
func copyTree(src, dst string) error {
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
				return fmt.Errorf("refusing to copy symlink %q → %q: target escapes profile source", path, link)
			}
			return os.Symlink(link, target)
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		default:
			return copyFile(path, target, info.Mode().Perm())
		}
	})
}

// symlinkWithin reports whether a symlink at linkPath pointing at linkTarget
// resolves to a path inside srcRoot. Accepts either absolute or relative
// linkTarget values.
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

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
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
