package profile

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dalley/ccp/internal/paths"
)

// DoctorFinding is one issue detected by Doctor — either a problem in a
// profile's on-disk state or the cross-profile manifest state.
type DoctorFinding struct {
	// Profile is the name of the profile the finding relates to, or "" if
	// the finding is global.
	Profile string
	// Severity is "warn" for recoverable issues and "error" for things that
	// would break switching to this profile.
	Severity string
	// Message is a human-readable description.
	Message string
	// Hint is optional actionable guidance.
	Hint string
}

// Doctor validates a profile's runtime directory against its source:
//   - runtime dir exists
//   - every symlink in the runtime dir points at a still-present source file
//   - no SharedItem entry that IS present in the source is missing a runtime symlink
//
// If profileName is empty, all profiles are checked.
func Doctor(p paths.Paths, profileName string) ([]DoctorFinding, error) {
	var profiles []Profile
	if profileName != "" {
		pr, err := NewChecked(p, profileName)
		if err != nil {
			return nil, err
		}
		if !pr.Exists() {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, profileName)
		}
		profiles = []Profile{pr}
	} else {
		got, err := List(p)
		if err != nil {
			return nil, err
		}
		profiles = got
	}

	var out []DoctorFinding
	for _, pr := range profiles {
		out = append(out, checkRuntime(pr)...)
	}
	return out, nil
}

func checkRuntime(pr Profile) []DoctorFinding {
	var out []DoctorFinding

	info, err := os.Stat(pr.ConfigDir)
	if err != nil {
		if os.IsNotExist(err) {
			out = append(out, DoctorFinding{
				Profile: pr.Name, Severity: "warn",
				Message: "runtime dir does not exist yet",
				Hint:    "run `ccp profile refresh " + pr.Name + "` to rebuild runtime symlinks",
			})
			return out
		}
		out = append(out, DoctorFinding{Profile: pr.Name, Severity: "error",
			Message: fmt.Sprintf("stat runtime dir: %v", err)})
		return out
	}
	if !info.IsDir() {
		out = append(out, DoctorFinding{Profile: pr.Name, Severity: "error",
			Message: "runtime path is not a directory",
			Hint:    "remove it and re-run `ccp profile create --from <other>`",
		})
		return out
	}

	// Pass 1: for each SharedItem present in the source, the runtime should
	// contain a matching symlink.
	for _, item := range SharedItems {
		src := filepath.Join(pr.SourceDir, item.Name)
		if _, err := os.Lstat(src); err != nil {
			continue // source doesn't have it; nothing to expect in runtime.
		}
		dst := filepath.Join(pr.ConfigDir, item.Name)
		info, err := os.Lstat(dst)
		if err != nil {
			out = append(out, DoctorFinding{Profile: pr.Name, Severity: "error",
				Message: fmt.Sprintf("missing runtime symlink for %s", item.Name),
				Hint:    "run `ccp profile rollback` or delete and recreate the profile",
			})
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			out = append(out, DoctorFinding{Profile: pr.Name, Severity: "warn",
				Message: fmt.Sprintf("%s is a regular file in runtime dir; expected symlink", item.Name),
				Hint:    "Claude may have overwritten a ccp symlink; move or delete the file and re-run",
			})
		}
	}

	// Pass 2: catch runtime symlinks whose source disappeared (dangling).
	entries, err := os.ReadDir(pr.ConfigDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		full := filepath.Join(pr.ConfigDir, e.Name())
		info, err := os.Lstat(full)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(full)
		if err != nil {
			continue
		}
		abs := target
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(filepath.Dir(full), abs)
		}
		// Only report dangling links that point into our source tree —
		// symlinks Claude created itself aren't our concern.
		rel, rerr := filepath.Rel(pr.SourceDir, abs)
		if rerr != nil || startsWithDotDot(rel) {
			continue
		}
		if _, err := os.Stat(abs); err != nil {
			out = append(out, DoctorFinding{Profile: pr.Name, Severity: "error",
				Message: fmt.Sprintf("dangling symlink for %s → %s", e.Name(), target),
				Hint:    "the source file was removed; recreate it or unlink the symlink",
			})
		}
	}
	return out
}
