//go:build windows

package profile

import "os"

// readFileNoFollow on Windows falls back to a plain ReadFile because
// syscall.O_NOFOLLOW is not defined under the Windows syscall package.
// The v2.0 Windows port is out of scope (see issue #6); Windows callers
// reach this helper only from the runtime-manifest pathway that the CLI
// layer disables at the command-registration level. The file exists so
// render.go compiles cross-platform.
func readFileNoFollow(path string) ([]byte, error) {
	return os.ReadFile(path)
}
