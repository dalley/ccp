//go:build !windows

package profile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// readFileNoFollow opens path with O_RDONLY|O_NOFOLLOW and returns its
// full contents. When path itself is a symlink, the kernel returns ELOOP
// and we surface a "refused to follow symlink" error — this is the
// per-file TOCTOU defence for BuildSymlinks/renderFile.
//
// rejectEscapingSymlinksInSource runs an initial pass and rejects symlinks
// whose target escapes the source tree. But between that walk and the
// actual read, an attacker with write access to the source tree could
// swap a non-escaping symlink for an escaping one. O_NOFOLLOW on every
// per-file read closes that race: we never traverse a symlink at all,
// even an intra-tree one, when reading content for rendering.
//
// Mirrors the allowlist.openNoFollow pattern. The Windows stub uses a
// plain ReadFile because syscall.O_NOFOLLOW isn't defined there; the v2.0
// Windows port is out of scope (see issue #6).
func readFileNoFollow(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if isNoFollowSymlinkErr(err) {
			return nil, fmt.Errorf("refused to follow symlink at %s", path)
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	return io.ReadAll(f)
}

// isNoFollowSymlinkErr reports whether err from OpenFile with O_NOFOLLOW
// indicates the target was itself a symlink. Linux and macOS both return
// ELOOP in this case. Mirrors internal/allowlist/allowlist.go:isSymlinkError.
func isNoFollowSymlinkErr(err error) bool {
	var pe *os.PathError
	if errors.As(err, &pe) {
		if errno, ok := pe.Err.(syscall.Errno); ok {
			return errno == syscall.ELOOP
		}
	}
	return false
}
