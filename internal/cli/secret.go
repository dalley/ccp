//go:build !windows

package cli

import "github.com/spf13/cobra"

// registerSecretCmd adds the `ccp secret` group to root. The Windows build
// variant in secret_windows.go is a no-op so root.go can call this
// unconditionally — build-tag-gated dispatch beats a runtime check because
// the newSecretCmd/child files themselves are `!windows` and wouldn't link
// under GOOS=windows.
func registerSecretCmd(root *cobra.Command) {
	root.AddCommand(newSecretCmd())
}

// newSecretCmd wires the `ccp secret` command group. Four child verbs mirror
// the internal/secret package surface: Set, Get, Delete, List.
func newSecretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage per-profile secrets stored in the OS keychain",
		Long: "Stores per-profile secrets in the OS keychain (macOS Keychain / Linux " +
			"Secret Service) with an atomic-file fallback for headless environments. " +
			"Reference a stored value from a profile file with the `{{ keychain:KEY }}` " +
			"template syntax.",
	}
	cmd.AddCommand(newSecretSetCmd())
	cmd.AddCommand(newSecretGetCmd())
	cmd.AddCommand(newSecretListCmd())
	cmd.AddCommand(newSecretRmCmd())
	return cmd
}
