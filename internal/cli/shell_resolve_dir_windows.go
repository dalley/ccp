//go:build windows

package cli

import "github.com/spf13/cobra"

// registerShellResolveDirCmd is a no-op on Windows. The resolver's
// allowlist and marker-read paths rely on POSIX-only machinery in v2.0,
// and the shell-init snippet that would invoke it is likewise POSIX/fish
// only. Listing the command in --help on Windows is worse than hiding it.
//
// When Windows gains a real marker + allowlist implementation, this file
// flips to the same `root.AddCommand(newShellResolveDirCmd())` body as
// its POSIX sibling.
func registerShellResolveDirCmd(_ *cobra.Command) {}
