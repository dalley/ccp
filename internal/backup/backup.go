// Package backup snapshots ccp state before destructive operations so that a
// bad switch or accidental delete is recoverable.
//
// A backup is just a timestamped directory under ~/.config/ccp/backups/
// containing whatever files/subtrees the caller moves or copies into it. This
// package owns naming and retention; individual commands own what to put in.
package backup

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultRetention is how many backups to keep by default.
const DefaultRetention = 10

// New creates a fresh backup directory tagged with op (e.g. "pre-delete-work")
// and returns its path. Caller populates it.
//
// Directory names include a 4-byte random suffix so rapid back-to-back
// backups within the same second cannot collide. Without the suffix, two
// `ccp profile delete` calls from a script run within 1s would MkdirAll
// the same directory and scramble each other's contents.
func New(baseDir, op string) (string, error) {
	ts := time.Now().UTC().Format("2006-01-02T15-04-05")
	name := ts + "_" + sanitize(op) + "_" + randomSuffix()
	dir := filepath.Join(baseDir, name)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", fmt.Errorf("create backups dir: %w", err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	return dir, nil
}

func randomSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to nanoseconds — still collision-resistant within a
		// single process for any plausible call rate.
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xFFFFFFFF)
	}
	return hex.EncodeToString(b[:])
}

// Prune deletes all but the most recent `keep` backups in baseDir.
// A keep of 0 leaves everything alone (pruning is opt-in via positive int).
func Prune(baseDir string, keep int) error {
	if keep <= 0 {
		return nil
	}
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	// Timestamps sort lexicographically thanks to the 2006-01-02T15-04-05
	// layout; newest last.
	sort.Strings(dirs)
	if len(dirs) <= keep {
		return nil
	}
	for _, old := range dirs[:len(dirs)-keep] {
		_ = os.RemoveAll(filepath.Join(baseDir, old))
	}
	return nil
}

// Latest returns the most recent backup directory, or "" if none.
func Latest(baseDir string) (string, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) == 0 {
		return "", nil
	}
	sort.Strings(dirs)
	return filepath.Join(baseDir, dirs[len(dirs)-1]), nil
}

// sanitize turns an operation label into something safe for a directory
// component: lowercase, alnum/dash only.
func sanitize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune('-')
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	out = strings.Trim(out, "-")
	if out == "" {
		out = "op"
	}
	return out
}
