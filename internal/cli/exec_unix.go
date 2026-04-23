//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// signalExitCode derives a POSIX-conventional exit code (128+signum) from
// a child killed by signal. Returns 128 when the signal can't be
// determined so the shell still sees a clearly-non-zero code rather than
// the 255 that os.Exit(-1) would produce.
func signalExitCode(ee *exec.ExitError) int {
	ws, ok := ee.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		return 128
	}
	if ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 128
}
