package cli

import (
	"github.com/dalley/ccp/internal/manifest"
	"github.com/dalley/ccp/internal/paths"
	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

// newShellActiveCmd prints the active profile name read through the real TOML
// parser, for use by the shell-init snippet. Separated from `ccp current` so
// shell callers bypass state.Ensure() (which would create directories the
// user may not want touched during shell startup) and so we never error out
// for routine reasons — a missing manifest, an unreadable file, or an
// unresolvable home all produce empty output + exit 0.
func newShellActiveCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "shell-active",
		Short:  "Print the active profile name (for use inside ccp shell-init snippets)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := paths.Resolve()
			if err != nil {
				return nil
			}
			m, _, err := manifest.Load(p.ManifestPath)
			if err != nil {
				return nil
			}
			if m.ActiveProfile == "" {
				return nil
			}
			if err := profile.ValidateName(m.ActiveProfile); err != nil {
				return nil
			}
			_, err = cmd.OutOrStdout().Write([]byte(m.ActiveProfile + "\n"))
			return err
		},
	}
}
