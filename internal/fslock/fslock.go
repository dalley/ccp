//go:build !windows

// Package fslock provides an advisory file lock used to serialize ccp state
// mutations. It wraps the POSIX flock(2) syscall via unix.Flock so two
// concurrent `ccp use` invocations can't clobber each other.
//
// Windows support is tracked in GitHub issue #6 and will live in a sibling
// fslock_windows.go using LockFileEx via golang.org/x/sys/windows.
package fslock

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// DefaultTimeout is the ceiling Acquire waits before returning
// ErrLockContended. Must exceed sync.NetworkTimeout (60s) so a long-running
// `ccp sync pull` doesn't starve every concurrent profile command — the
// starvation produces ExitConflict which agents interpret as a real profile
// conflict, not a wait.
const DefaultTimeout = 90 * time.Second

// ErrLockContended is returned when Acquire's deadline elapses without
// getting the lock. Callers use errors.Is to surface a retry hint.
type ErrLockContended struct{ Path string }

func (e *ErrLockContended) Error() string {
	return fmt.Sprintf("another ccp process is holding %s; wait or kill it and retry", e.Path)
}

// Lock represents an acquired advisory lock.
type Lock struct {
	f *os.File
}

// Acquire takes an exclusive lock on path, creating the file if necessary.
// It retries with LOCK_NB until DefaultTimeout elapses. Use AcquireWithTimeout
// to pick a different deadline.
func Acquire(path string) (*Lock, error) {
	return AcquireWithTimeout(path, DefaultTimeout)
}

// AcquireWithTimeout is Acquire with an explicit deadline. A non-positive
// timeout falls back to a single LOCK_NB attempt (non-blocking).
func AcquireWithTimeout(path string, timeout time.Duration) (*Lock, error) {
	// O_CLOEXEC prevents child processes spawned by `ccp exec` from
	// inheriting the lock FD and keeping it held after ccp itself exits.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}

	deadline := time.Now().Add(timeout)
	first := true
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &Lock{f: f}, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			_ = f.Close()
			return nil, fmt.Errorf("flock %s: %w", path, err)
		}
		if timeout <= 0 || (!first && time.Now().After(deadline)) {
			_ = f.Close()
			return nil, &ErrLockContended{Path: path}
		}
		first = false
		time.Sleep(100 * time.Millisecond)
	}
}

// Release unlocks and closes the lock file.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	if err := unix.Flock(int(l.f.Fd()), unix.LOCK_UN); err != nil {
		_ = l.f.Close()
		return err
	}
	return l.f.Close()
}
