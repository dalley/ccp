//go:build !windows

package cli

import (
	"errors"
	"fmt"

	"github.com/dalley/ccp/internal/profile"
	"github.com/dalley/ccp/internal/secret"
	"github.com/spf13/cobra"
)

// newSecretRmCmd wires `ccp secret rm <profile> <key>`.
//
// Idempotent: removing a nonexistent key succeeds silently (secret.Delete
// already swallows NotFound from both backends). No confirmation prompt —
// removal from the keychain is low-stakes: the user can re-set. Matches the
// plan's "single-key rm is safe" stance vs the bulk-rm case we explicitly
// punted.
//
// Goes through withLockedState so a concurrent `secret set` + `secret rm`
// pair against the file-fallback store don't race on the index/fallback
// files. Same discipline as `secret set`.
func newSecretRmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm <profile> <key>",
		Short: "Remove the stored secret for <profile>/<key> (idempotent)",
		Long: "Removes the secret for <profile>/<key> from both the keychain and the " +
			"file-fallback store. Idempotent: removing a nonexistent key succeeds " +
			"silently with a 'no secret stored' message. No confirmation prompt — " +
			"removal from the keychain is low-stakes (re-set is cheap). A locked " +
			"keychain returns a typed error rather than skipping the backend.",
		Example: "  # remove a single key\n" +
			"  ccp secret rm work API_KEY\n\n" +
			"  # safe to run against a key that was never set\n" +
			"  ccp secret rm work MAYBE_SET",
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
			// Peek before mutating so we can emit a differentiated
			// message ("Removed" vs "No secret stored"). Mirrors the
			// ccp deny pattern in allow.go. The peek is lock-free: a
			// concurrent writer could change the state between peek and
			// Delete, but Delete is idempotent so the end state is the
			// same — worst case the message is slightly stale.
			_, peekErr := secret.Get(s.Paths, prof, key)
			existed := peekErr == nil
			// If Get returned anything other than a clean hit or
			// ErrSecretNotFound, surface the error rather than trying
			// to Delete under an unknown backend state (e.g. keychain
			// locked — user needs to unlock, not blunder on).
			if !existed && !errors.Is(peekErr, secret.ErrSecretNotFound) {
				return peekErr
			}
			if err := withLockedState(s.Paths, func(s *state) error {
				return secret.Delete(s.Paths, prof, key)
			}); err != nil {
				return err
			}
			if existed {
				fmt.Fprintf(cmd.OutOrStdout(), "Removed secret %s/%s\n", prof, key)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "No secret %s/%s stored\n", prof, key)
			}
			return nil
		},
	}
	return cmd
}
