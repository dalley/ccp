package sync

import (
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

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
