package cli

import (
	"errors"

	"github.com/dalley/ccp/internal/fslock"
	"github.com/dalley/ccp/internal/profile"
	ccpsync "github.com/dalley/ccp/internal/sync"
)

// Exit codes emitted by the ccp binary. Agents branch on these to choose
// their next action (retry, escalate, stop). Keep stable — document in
// README when adding a new one.
const (
	ExitOK       = 0
	ExitUser     = 1 // caller error: bad name, missing required arg, unknown profile, etc.
	ExitState    = 2 // local IO / state corruption: unreadable manifest, disk full, permission denied
	ExitNetwork  = 3 // remote or git-over-network failure
	ExitConflict = 4 // something already exists, tree is dirty, lock is held by another process
)

// ExitCodeFor classifies err for the shell. It walks the error chain via
// errors.Is/errors.As so wrapped errors still map correctly.
func ExitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	switch {
	case errors.Is(err, profile.ErrNotFound):
		return ExitUser
	case errors.Is(err, profile.ErrAlreadyExists):
		return ExitConflict
	case errors.Is(err, ccpsync.ErrDirtyWorkingTree):
		return ExitConflict
	case errors.Is(err, ccpsync.ErrRemoteUnreachable):
		return ExitNetwork
	}
	var lockErr *fslock.ErrLockContended
	if errors.As(err, &lockErr) {
		return ExitConflict
	}
	return ExitUser
}
