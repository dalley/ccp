package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// ErrDiffFound is returned by the CLI layer when `ccp profile diff` finds
// any differences. Agents use this sentinel via exit code 4 (conflict) to
// branch on "act on the diff" vs "nothing to do" without parsing stdout.
var ErrDiffFound = errors.New("profiles differ")

// DiffEntry is one file or directory difference between two profiles.
type DiffEntry struct {
	// Path is relative to the profile source directory.
	Path string `json:"path"`
	// Kind describes the difference.
	Kind DiffKind `json:"kind"`
	// HashA / HashB are SHA-256 hex digests; empty when the file is missing
	// on that side.
	HashA string `json:"hash_a,omitempty"`
	HashB string `json:"hash_b,omitempty"`
}

// DiffKind classifies a DiffEntry.
type DiffKind string

const (
	DiffOnlyInA      DiffKind = "only-in-a"
	DiffOnlyInB      DiffKind = "only-in-b"
	DiffChanged      DiffKind = "changed"
	DiffTypeMismatch DiffKind = "type-mismatch" // one side file, other side dir
)

// Diff recursively compares two profile source directories.
// Returns entries sorted by Path for stable output. A nil slice with nil err
// means the two trees are identical.
func Diff(a, b Profile) ([]DiffEntry, error) {
	aFiles, err := hashTree(a.SourceDir)
	if err != nil {
		return nil, fmt.Errorf("hash %s: %w", a.Name, err)
	}
	bFiles, err := hashTree(b.SourceDir)
	if err != nil {
		return nil, fmt.Errorf("hash %s: %w", b.Name, err)
	}

	// Union of paths.
	seen := map[string]struct{}{}
	for p := range aFiles {
		seen[p] = struct{}{}
	}
	for p := range bFiles {
		seen[p] = struct{}{}
	}
	var paths []string
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var out []DiffEntry
	for _, p := range paths {
		ha, okA := aFiles[p]
		hb, okB := bFiles[p]
		switch {
		case okA && !okB:
			out = append(out, DiffEntry{Path: p, Kind: DiffOnlyInA, HashA: ha})
		case !okA && okB:
			out = append(out, DiffEntry{Path: p, Kind: DiffOnlyInB, HashB: hb})
		case ha != hb:
			// Special-case the type-mismatch sentinel (see hashTree) so
			// users get a clearer label than "changed".
			kind := DiffChanged
			if ha == "<dir>" || hb == "<dir>" {
				kind = DiffTypeMismatch
			}
			out = append(out, DiffEntry{Path: p, Kind: kind, HashA: ha, HashB: hb})
		}
	}
	return out, nil
}

// hashTree returns a map from relative path to sha256 hex digest for every
// regular file under root. Directories are represented implicitly by the
// files beneath them; empty directories are represented explicitly with the
// sentinel hash "<dir>" so that a file-on-one-side / dir-on-the-other
// conflict is detectable.
func hashTree(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		switch {
		case info.IsDir():
			// Tag empty dirs so they appear in the diff.
			entries, _ := os.ReadDir(path)
			if len(entries) == 0 {
				out[rel] = "<dir>"
			}
			return nil
		case info.Mode().IsRegular():
			h, herr := hashFile(path)
			if herr != nil {
				return herr
			}
			out[rel] = h
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return out, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
