//go:build !windows

package cli

import (
	"encoding/json"
	"fmt"

	"github.com/dalley/ccp/internal/profile"
	"github.com/dalley/ccp/internal/secret"
	"github.com/spf13/cobra"
)

// newSecretListCmd wires `ccp secret list <profile> [--json]`.
//
// Default output: keys one per line (stable sort handled by secret.List).
// --json output: {"profile":"work","keys":["A","B"]}. The object-with-keys
// shape (not a bare array) leaves room for future fields — e.g. per-key
// backend provenance ("keychain"/"file") — without breaking compat.
//
// Read-only: no withLockedState. See get's rationale.
func newSecretListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list <profile>",
		Short: "List the secret keys stored for <profile>",
		Long: "Lists secret keys stored for <profile>, merging the keychain-index " +
			"and the file-fallback store. Default output is one key per line, " +
			"sorted alphabetically. --json emits {\"profile\":\"<name>\", \"keys\":[...]} " +
			"— the object-with-keys shape (not a bare array) leaves room for future " +
			"per-key metadata without breaking parse compat.",
		Example: "  # one key per line\n" +
			"  ccp secret list work\n\n" +
			"  # structured output for scripting\n" +
			"  ccp secret list work --json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProfileName,
		RunE: func(cmd *cobra.Command, args []string) error {
			prof := args[0]
			if err := profile.ValidateName(prof); err != nil {
				return err
			}
			s, err := loadState()
			if err != nil {
				return err
			}
			keys, err := secret.List(s.Paths, prof)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				if keys == nil {
					keys = []string{}
				}
				payload := struct {
					Profile string   `json:"profile"`
					Keys    []string `json:"keys"`
				}{
					Profile: prof,
					Keys:    keys,
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(payload)
			}
			for _, k := range keys {
				fmt.Fprintln(out, k)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit {\"profile\":..., \"keys\":[...]}")
	return cmd
}
