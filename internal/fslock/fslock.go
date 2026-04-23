// Package fslock provides an advisory file lock used to serialize ccp state
// mutations. It wraps the POSIX flock(2) syscall via unix.Flock so two
// concurrent `ccp use` invocations can't clobber each other.
package fslock

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Lock represents an acquired advisory lock.
type Lock struct {
	f *os.File
}

// Acquire takes an exclusive lock on path, creating the file if necessary.
// It blocks until the lock is available. Call Release() when done.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
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
