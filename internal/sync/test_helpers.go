package sync

import (
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// contains is a tiny wrapper so sync_test.go doesn't need to import strings
// and can stay focused on sync behavior.
func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

// initBare creates a bare repository suitable for tests to push against.
// Kept here rather than in sync.go because production code doesn't need to
// create bare repos.
func initBare(path string) error {
	_, err := git.PlainInitWithOptions(path, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.Main},
		Bare:        true,
	})
	return err
}
