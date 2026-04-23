package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dalley/ccp/internal/paths"
)

// CreateOptions controls profile creation.
type CreateOptions struct {
	// FromCurrent seeds the profile by copying the shared items from
	// ~/.claude/ into the new profile's source directory.
	FromCurrent bool
	// FromProfile, when non-empty, seeds from the named existing profile.
	// Mutually exclusive with FromCurrent.
	FromProfile string
}

// ErrAlreadyExists is returned when a Create target already exists.
var ErrAlreadyExists = errors.New("profile already exists")

// ErrNotFound is returned when an operation targets a missing profile.
var ErrNotFound = errors.New("profile not found")

// Create materializes a new profile: source dir + runtime dir + symlinks.
// Does not touch any shellrc — call InstallAlias separately if desired.
func Create(p paths.Paths, name string, opts CreateOptions) (Profile, error) {
	if err := ValidateName(name); err != nil {
		return Profile{}, err
	}
	if opts.FromCurrent && opts.FromProfile != "" {
		return Profile{}, fmt.Errorf("--from-current and --from are mutually exclusive")
	}

	pr := New(p, name)
	if pr.Exists() {
		return pr, fmt.Errorf("%w: %s", ErrAlreadyExists, name)
	}

	// O_EXCL-style guard against concurrent Create races. Any error other
	// than IsExist is fatal.
	if err := os.Mkdir(pr.SourceDir, 0o755); err != nil {
		if os.IsExist(err) {
			return pr, fmt.Errorf("%w: %s", ErrAlreadyExists, name)
		}
		return pr, fmt.Errorf("create source dir: %w", err)
	}
	// On any later failure, roll back the source dir so a retry can succeed.
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(pr.SourceDir)
		}
	}()

	switch {
	case opts.FromCurrent:
		if err := seedFromCurrent(p, pr); err != nil {
			return pr, err
		}
	case opts.FromProfile != "":
		src := New(p, opts.FromProfile)
		if !src.Exists() {
			return pr, fmt.Errorf("%w: %s (source for --from)", ErrNotFound, opts.FromProfile)
		}
		if err := copyTree(src.SourceDir, pr.SourceDir); err != nil {
			return pr, fmt.Errorf("copy from %s: %w", opts.FromProfile, err)
		}
	}

	if err := pr.BuildSymlinks(); err != nil {
		return pr, err
	}

	success = true
	return pr, nil
}

// seedFromCurrent copies every SharedItems entry present in ~/.claude/ into
// pr.SourceDir. Skips missing items silently — a user who has never created a
// keybindings.json gets an empty slot, not an error.
func seedFromCurrent(p paths.Paths, pr Profile) error {
	for _, item := range SharedItems {
		src := filepath.Join(p.ClaudeHome, item.Name)
		dst := filepath.Join(pr.SourceDir, item.Name)
		info, err := os.Lstat(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("stat %s: %w", src, err)
		}
		if info.IsDir() {
			if err := copyTree(src, dst); err != nil {
				return fmt.Errorf("copy %s: %w", src, err)
			}
		} else {
			if err := copyFile(src, dst, info.Mode().Perm()); err != nil {
				return fmt.Errorf("copy %s: %w", src, err)
			}
		}
	}
	return nil
}

// Delete removes a profile's source dir and any ccp-managed symlinks in its
// runtime dir. It does NOT remove runtime data created by Claude itself
// (auth, sessions, caches) — those are moved into the backup for safety.
//
// Returns the backup path so the caller can surface it.
func Delete(p paths.Paths, name string, backupDir string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	pr := New(p, name)
	if !pr.Exists() {
		return "", fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	// Move source dir into the backup so delete is reversible.
	srcBackup := filepath.Join(backupDir, "profiles-"+name)
	if err := os.Rename(pr.SourceDir, srcBackup); err != nil {
		return "", fmt.Errorf("backup source: %w", err)
	}

	// Move runtime dir (if it exists) into the backup as well. Whole-dir
	// rename captures auth tokens/sessions/caches along for the ride.
	if _, err := os.Stat(pr.ConfigDir); err == nil {
		runtimeBackup := filepath.Join(backupDir, "claude-"+name)
		if err := os.Rename(pr.ConfigDir, runtimeBackup); err != nil {
			return "", fmt.Errorf("backup runtime: %w", err)
		}
	}

	return backupDir, nil
}

// Rename moves a profile's source and runtime dirs to a new name. It does
// NOT touch alias blocks — callers update those in the CLI layer.
func Rename(p paths.Paths, oldName, newName string) error {
	if err := ValidateName(oldName); err != nil {
		return fmt.Errorf("old name: %w", err)
	}
	if err := ValidateName(newName); err != nil {
		return fmt.Errorf("new name: %w", err)
	}
	src := New(p, oldName)
	dst := New(p, newName)
	if !src.Exists() {
		return fmt.Errorf("%w: %s", ErrNotFound, oldName)
	}
	if dst.Exists() {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, newName)
	}

	if err := os.Rename(src.SourceDir, dst.SourceDir); err != nil {
		return fmt.Errorf("rename source: %w", err)
	}
	if _, err := os.Stat(src.ConfigDir); err == nil {
		if err := os.Rename(src.ConfigDir, dst.ConfigDir); err != nil {
			return fmt.Errorf("rename runtime: %w", err)
		}
	}
	// Re-point symlinks — the OS stores them by path string, not inode.
	if err := dst.RemoveSymlinks(); err != nil {
		return err
	}
	return dst.BuildSymlinks()
}
