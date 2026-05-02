package cli

import (
	"fmt"
	"io"

	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

func newProfileUseCmd() *cobra.Command {
	var shellOnly bool
	cmd := &cobra.Command{
		Use:               "use <name>",
		Short:             "Set the active profile",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProfileName,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := profile.ValidateName(name); err != nil {
				return err
			}
			s, err := loadState()
			if err != nil {
				return err
			}
			pr := profile.New(s.Paths, name)

			if shellOnly {
				if !pr.Exists() {
					return fmt.Errorf("profile %q not found", name)
				}
				// Emit a line the caller can eval to set env for the current
				// shell — same idiom as `ssh-agent -s` or `nvm use` returning
				// a function call.
				// shellQuote both values so a ConfigDir containing
				// `$` or `'` (exotic HOMEs, or a CCP_ROOT under a path
				// with spaces) is safe to eval in /bin/sh. The profile
				// name is already regex-validated, but we quote it too
				// for consistency.
				fmt.Fprintf(cmd.OutOrStdout(), "export CLAUDE_CONFIG_DIR=%s\nexport CCP_PROFILE=%s\n",
					shellQuote(pr.ConfigDir), shellQuote(name))
				return nil
			}

			// We compute shouldEmit INSIDE the locked state (priorVersion
			// is read there) but EMIT the advisory only AFTER the lock
			// releases AND manifest.Save has succeeded. If manifest.Save
			// fails, withLockedState returns a non-nil error and we fall
			// out via the `return err` below — the advisory stays silent,
			// which is the correct behavior because LastSeenVersion was
			// not persisted and the user would see the advisory again on
			// the next `ccp use`. Guards Finding #6 from round 2 review.
			var (
				priorVersion string
				shouldEmit   bool
			)
			err = withLockedState(s.Paths, func(s *state) error {
				// Re-check existence under the lock: a concurrent delete
				// could have removed the profile between loadState and
				// lock acquisition.
				if !profile.New(s.Paths, name).Exists() {
					return fmt.Errorf("profile %q not found (create it with: ccp profile create %s)", name, name)
				}
				priorVersion = s.Manifest.LastSeenVersion
				shouldEmit = priorVersion != Version
				s.Manifest.ActiveProfile = name
				// Stamp the current binary version so subsequent `ccp use`
				// invocations know we've already greeted this manifest. The
				// stamp is written in the same manifest.Save as
				// ActiveProfile — one atomic rename, no split-brain if the
				// process dies between the two fields.
				s.Manifest.LastSeenVersion = Version
				return nil
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Active profile: %s\n", name)
			fmt.Fprintln(cmd.OutOrStdout(),
				"New shells will pick this up. In this shell, run: eval \"$(ccp use "+name+" --shell)\"")
			// Migration advisory — stderr only, non-fatal. Gated by
			// shouldEmit (set only when we observed a version mismatch
			// AND successfully persisted the new stamp above).
			if shouldEmit {
				emitMigrationAdvisory(cmd.ErrOrStderr(), priorVersion, Version)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&shellOnly, "shell", false, "emit `export` lines for the current shell instead of changing the global active profile")
	return cmd
}

// emitMigrationAdvisory prints a one-time stderr notice when the manifest's
// recorded ccp version doesn't match the running binary. Two tiers:
//
//  1. Empty prior version → first post-v2 upgrade. Give the full
//     secrets-migration pointer because the user may have cleartext keys in
//     their profile files that v2's keychain support now lets them move.
//  2. Non-empty but different prior version → any later upgrade. Keep it
//     short; the user has already seen the v2 message and just needs a
//     reminder that `ccp profile audit` exists.
//
// Callers are responsible for gating on "versions differ AND save
// succeeded" — see the shouldEmit variable in the use command. The
// equality short-circuit here is belt-and-suspenders so a future caller
// can't accidentally fire the advisory when it shouldn't.
func emitMigrationAdvisory(w io.Writer, priorVersion, currentVersion string) {
	if priorVersion == currentVersion {
		return
	}
	if priorVersion == "" {
		fmt.Fprintln(w, "ccp: v2.0 adds secrets separation and auto-activation. "+
			"If any of your profile files contain cleartext API keys, run `ccp profile audit` "+
			"to find them and `ccp secret set` to migrate them to the OS keychain. "+
			"See the README for details.")
		return
	}
	fmt.Fprintf(w, "ccp: upgraded from %s to %s — run `ccp profile audit` to check for cleartext secrets worth migrating.\n",
		priorVersion, currentVersion)
}
