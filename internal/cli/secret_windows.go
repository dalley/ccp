//go:build windows

package cli

import "github.com/spf13/cobra"

// registerSecretCmd is a no-op on Windows. The internal/secret package's
// Windows stubs return ErrUnsupportedPlatform for every operation in v2.0,
// so we hide the `ccp secret` command group from --help on that platform
// rather than ship a set of commands that can't do anything useful.
//
// When Windows gains a real DPAPI / Credential Manager backend (see
// internal/secret/secret_windows.go), this file flips to the same
// `root.AddCommand(newSecretCmd())` body as its POSIX sibling.
func registerSecretCmd(_ *cobra.Command) {}
