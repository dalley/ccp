//go:build windows

package cli

import "github.com/spf13/cobra"

// registerAllowCmd is a no-op on Windows. The allow/deny commands rely on
// the same marker-reading and allowlist machinery that ultimately surfaces
// ErrUnsupportedPlatform on Windows in v2.0, and listing them in --help on
// a platform where they can't work is worse than hiding them.
//
// When Windows gains a real marker + allowlist implementation, this file
// flips to the same `root.AddCommand(newAllowCmd())` /
// `root.AddCommand(newDenyCmd())` body as its POSIX sibling.
func registerAllowCmd(_ *cobra.Command) {}
