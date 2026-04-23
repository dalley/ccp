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
				fmt.Fprintf(cmd.OutOrStdout(), "export CLAUDE_CONFIG_DIR=%q\nexport CCP_PROFILE=%s\n",
					pr.ConfigDir, name)
				return nil
			}

			var priorVersion string
			err = withLockedState(s.Paths, func(s *state) error {
				// Re-check existence under the lock: a concurrent delete
				// could have removed the profile between loadState and
				// lock acquisition.
				if !profile.New(s.Paths, name).Exists() {
					return fmt.Errorf("profile %q not found (create it with: ccp profile create %s)", name, name)
				}
				priorVersion = s.Manifest.LastSeenVersion
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
			// Migration advisory — stderr only, non-fatal. Fires at most
			// once per manifest because withLockedState saves the new
			// LastSeenVersion on success.
			emitMigrationAdvisory(cmd.ErrOrStderr(), priorVersion, Version)
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
// Silent on match (the common case once everyone's on v2+).
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
