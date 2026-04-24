package cli

import (
	"io"

	ccpsync "github.com/dalley/ccp/internal/sync"
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
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// Silence SSH auth warnings when ANY command runs with --json
			// so structured output isn't interleaved with prose. The flag
			// is looked up defensively: commands that don't define it
			// just fall through.
			if jf := cmd.Flags().Lookup("json"); jf != nil && jf.Value.String() == "true" {
				ccpsync.SetAuthWarnWriter(io.Discard)
			}
			return nil
		},
	}

	root.AddCommand(newVersionCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newShellInitCmd())
	root.AddCommand(newShellActiveCmd())
	// shell-resolve-dir, allow/deny, and secret are all platform-gated for
	// the same reason: the internal machinery they wrap returns
	// ErrUnsupportedPlatform on Windows in v2.0, and listing commands in
	// --help on a platform where they can't work is worse than hiding them.
	// Each register* helper is a no-op on Windows (see the *_windows.go
	// siblings) and AddCommand(newXCmd()) on POSIX.
	registerShellResolveDirCmd(root)
	root.AddCommand(newProfileCmd())
	root.AddCommand(newCurrentCmd())
	// `ccp use <name>` is a shortcut for `ccp profile use <name>` — the
	// word "profile" is redundant on the hot path.
	root.AddCommand(newProfileUseCmd())
	root.AddCommand(newExecCmd())
	root.AddCommand(newSyncCmd())
	root.AddCommand(newPromptCmd())
	registerAllowCmd(root)
	registerSecretCmd(root)

	return root
}
