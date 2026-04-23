//go:build windows

package cli

import (
	"errors"
	"io/fs"

	"github.com/dalley/ccp/internal/fslock"
	"github.com/dalley/ccp/internal/profile"
	ccpsync "github.com/dalley/ccp/internal/sync"
)

// ExitCodeFor on Windows skips the POSIX syscall error mapping because
// constants like ENOSPC/EROFS/EIO aren't exported by syscall there. The
// equivalent NTSTATUS codes arrive as os.PathError-wrapped errors — we
// classify those conservatively. Expand once Windows is supported for real
// (see issue #6).
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
	case errors.Is(err, fs.ErrPermission):
		return ExitState
	}
	return ExitUser
}
