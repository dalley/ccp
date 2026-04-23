//go:build !windows

package cli

import (
	"errors"
	"io/fs"
	"strings"
	"syscall"

	"github.com/dalley/ccp/internal/allowlist"
	"github.com/dalley/ccp/internal/fslock"
	"github.com/dalley/ccp/internal/profile"
	"github.com/dalley/ccp/internal/refs"
	"github.com/dalley/ccp/internal/secret"
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
	case errors.Is(err, profile.ErrDiffFound):
		return ExitConflict
	case errors.Is(err, profile.ErrDoctorFailed):
		return ExitState
	case errors.Is(err, profile.ErrAuditSecretsDetected):
		return ExitConflict
	case errors.Is(err, secret.ErrSecretNotFound):
		return ExitUser
	case errors.Is(err, secret.ErrKeychainLocked),
		errors.Is(err, secret.ErrKeychainUnavailable),
		errors.Is(err, secret.ErrUnsupportedPlatform):
		return ExitState
	case errors.Is(err, refs.ErrSecretRefUnresolved),
		errors.Is(err, refs.ErrUnsupportedPlatform):
		return ExitState
	case errors.Is(err, allowlist.ErrMarkerNotAllowed),
		errors.Is(err, allowlist.ErrMarkerHashMismatch):
		return ExitConflict
	case errors.Is(err, allowlist.ErrInvalidMarker):
		return ExitUser
	case errors.Is(err, allowlist.ErrUnsupportedPlatform):
		return ExitState
	case errors.Is(err, ccpsync.ErrDirtyWorkingTree):
		return ExitConflict
	case errors.Is(err, ccpsync.ErrRemoteUnreachable):
		return ExitNetwork
	}
	var lockErr *fslock.ErrLockContended
	if errors.As(err, &lockErr) {
		return ExitConflict
	}
	switch {
	case errors.Is(err, fs.ErrExist):
		return ExitConflict
	case errors.Is(err, fs.ErrPermission),
		errors.Is(err, syscall.ENOSPC),
		errors.Is(err, syscall.EROFS),
		errors.Is(err, syscall.EIO):
		return ExitState
	}
	// An unrecognized error that mentions an internal ccp concern
	// (manifest, symlink, backup, lock) is more likely a state/IO issue
	// than a caller error. Misclassifying these as ExitUser leads agents
	// to retry with different arguments when they should escalate.
	msg := err.Error()
	for _, hint := range []string{"manifest", "symlink", "backup", "lock file", "filesystem changes committed"} {
		if strings.Contains(msg, hint) {
			return ExitState
		}
	}
	return ExitUser
}
