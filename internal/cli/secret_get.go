//go:build !windows

package cli

import (
	"fmt"

	"github.com/dalley/ccp/internal/profile"
	"github.com/dalley/ccp/internal/secret"
	"github.com/spf13/cobra"
)

// newSecretGetCmd wires `ccp secret get <profile> <key>`.
//
// Prints the resolved value to stdout with NO trailing newline. This is the
// scriptable contract agents rely on — users pipe through `$(...)` and a
// trailing '\n' would bleed into the consumer's variable (shells DO strip
// trailing newlines from $(...) but the same value consumed by `read -r`
// or fed into a Go program doesn't get that implicit strip). Errors go to
// stderr via cobra's standard error handling.
//
// Read-only: no withLockedState. Concurrent Gets against the keychain and
// file-fallback store are safe — see invariant 2 on internal/secret/secret.go
// ("pure file I/O + keyring calls; does NOT take the ccp state lock").
func newSecretGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "get <profile> <key>",
		Short:             "Print the stored value for <profile>/<key> (no trailing newline)",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeProfileName,
		RunE: func(cmd *cobra.Command, args []string) error {
			prof := args[0]
			key := args[1]
			if err := profile.ValidateName(prof); err != nil {
				return err
			}
			if err := secret.ValidateKey(key); err != nil {
				return err
			}
			s, err := loadState()
			if err != nil {
				return err
			}
			value, err := secret.Get(s.Paths, prof, key)
			if err != nil {
				return err
			}
			// Fprintf (not Fprintln) intentional — no trailing newline.
			_, err = fmt.Fprint(cmd.OutOrStdout(), value)
			return err
		},
	}
	return cmd
}
