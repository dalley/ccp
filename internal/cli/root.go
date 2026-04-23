package cli

import (
	"github.com/spf13/cobra"
)

// NewRoot builds the top-level cobra command tree.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "ccp",
		Short:         "Manage multiple Claude Code profiles",
		Long:          "ccp keeps multiple named Claude Code configurations on one machine and switches between them.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newVersionCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newShellInitCmd())
	root.AddCommand(newProfileCmd())
	root.AddCommand(newCurrentCmd())
	// `ccp use <name>` is a shortcut for `ccp profile use <name>` — the
	// word "profile" is redundant on the hot path.
	root.AddCommand(newProfileUseCmd())
	root.AddCommand(newExecCmd())

	return root
}
