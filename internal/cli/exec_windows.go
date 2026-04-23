//go:build windows

package cli

import "os/exec"

// signalExitCode has no POSIX-signal equivalent on Windows. Fall back to
// 128 so callers still see a clearly-failing exit code. Proper Windows
// NTSTATUS mapping lands with Windows support (issue #6).
func signalExitCode(_ *exec.ExitError) int { return 128 }
