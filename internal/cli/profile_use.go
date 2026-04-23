package cli

import (
	"fmt"

	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

func newProfileUseCmd() *cobra.Command {
	var shellOnly bool
	cmd := &cobra.Command{
		Use:   "use <name>",
		Short: "Set the active profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			s, err := loadState()
			if err != nil {
				return err
			}
			pr := profile.New(s.Paths, name)
			if !pr.Exists() {
				return fmt.Errorf("profile %q not found (create it with: ccp profile create %s)", name, name)
			}

			if shellOnly {
				// Emit a line the caller can eval to set env for the current
				// shell — same idiom as `ssh-agent -s` or `nvm use` returning
				// a function call.
				fmt.Fprintf(cmd.OutOrStdout(), "export CLAUDE_CONFIG_DIR=%q\nexport CCP_PROFILE=%s\n",
					pr.ConfigDir, name)
				return nil
			}

			err = withLock(s.Paths, func() error {
				s.Manifest.ActiveProfile = name
				return saveManifest(s)
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Active profile: %s\n", name)
			fmt.Fprintln(cmd.OutOrStdout(),
				"New shells will pick this up. In this shell, run: eval \"$(ccp use "+name+" --shell)\"")
			return nil
		},
	}
	cmd.Flags().BoolVar(&shellOnly, "shell", false, "emit `export` lines for the current shell instead of changing the global active profile")
	return cmd
}
