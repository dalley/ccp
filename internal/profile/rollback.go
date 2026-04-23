package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dalley/ccp/internal/paths"
)

// Rollback restores the most recent backup made by Delete. Each backup dir
// contains `profiles-<name>/` and optionally `claude-<name>/` — this walks
// them and moves the contents back to their original locations.
//
// Returns the profile names restored and the backup dir that was consumed.
// If the destination already exists (user created a new profile with the
// same name in the meantime), an error is returned and nothing is moved.
func Rollback(p paths.Paths, backupDir string) ([]string, error) {
	info, err := os.Stat(backupDir)
	if err != nil {
		return nil, fmt.Errorf("stat backup %s: %w", backupDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", backupDir)
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return nil, err
	}

	// Plan the moves first so we can surface conflicts before touching the
	// filesystem. Each entry maps to a final destination.
	type move struct{ from, to, name string; isSource bool }
	var moves []move
	var restored []string
	seen := map[string]struct{}{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "profiles-"):
			profileName := strings.TrimPrefix(name, "profiles-")
			if err := ValidateName(profileName); err != nil {
				continue
			}
			to := p.ProfileSourceDir(profileName)
			if exists(to) {
				return nil, fmt.Errorf("cannot restore profile %q: source already exists at %s",
					profileName, to)
			}
			moves = append(moves, move{
				from: filepath.Join(backupDir, name), to: to,
				name: profileName, isSource: true,
			})
			if _, ok := seen[profileName]; !ok {
				restored = append(restored, profileName)
				seen[profileName] = struct{}{}
			}
		case strings.HasPrefix(name, "claude-"):
			profileName := strings.TrimPrefix(name, "claude-")
			if err := ValidateName(profileName); err != nil {
				continue
			}
			to := p.ProfileConfigDir(profileName)
			if exists(to) {
				return nil, fmt.Errorf("cannot restore runtime for %q: dir already exists at %s",
					profileName, to)
			}
			moves = append(moves, move{
				from: filepath.Join(backupDir, name), to: to,
				name: profileName, isSource: false,
			})
		}
	}
	if len(moves) == 0 {
		return nil, errors.New("nothing to restore in backup")
	}

	// Execute moves. First failure aborts — partial restore is OK because
	// each moved profile is independently valid and the remainder stays in
	// the backup dir.
	for _, mv := range moves {
		if err := os.Rename(mv.from, mv.to); err != nil {
			return nil, fmt.Errorf("restore %s: %w", mv.name, err)
		}
	}

	// Clean up the backup dir if it's now empty (rename-based moves emptied
	// everything we care about).
	_ = removeEmptyDir(backupDir)

	return restored, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func removeEmptyDir(d string) error {
	entries, err := os.ReadDir(d)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return nil
	}
	return os.Remove(d)
}
