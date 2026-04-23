package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

func newExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec <profile> -- <command...>",
		Short: "Run a command with CLAUDE_CONFIG_DIR set to the given profile",
		Long: `exec runs <command...> with CLAUDE_CONFIG_DIR pointing at <profile>'s
runtime directory, without changing the global active profile. Useful for
scripts, CI, or running a one-off command against a non-active profile.

Use -- to separate the profile name from the command arguments.

Examples:
  ccp exec work -- claude --help
  ccp exec demo -- claude mcp list
`,
		Args:               cobra.MinimumNArgs(2),
		DisableFlagParsing: true, // forward every flag unchanged to the child process.
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			// exec only completes the profile name in the first position;
			// after that the child's own completion (if any) takes over.
			if len(args) == 0 {
				return completeProfileName(cmd, args, toComplete)
			}
			return nil, cobra.ShellCompDirectiveDefault
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Cobra strips a bare "--" from args when DisableFlagParsing is
			// true, but positional ordering is preserved.
			name := args[0]
			rest := args[1:]
			if len(rest) > 0 && rest[0] == "--" {
				rest = rest[1:]
			}
			if len(rest) == 0 {
				return fmt.Errorf("no command given after profile name")
			}

			s, err := loadState()
			if err != nil {
				return err
			}
			pr, err := profile.NewChecked(s.Paths, name)
			if err != nil {
				return err
			}
			if !pr.Exists() {
				return fmt.Errorf("profile %q not found", name)
			}

			child := exec.Command(rest[0], rest[1:]...)
			child.Stdin = os.Stdin
			child.Stdout = cmd.OutOrStdout()
			child.Stderr = cmd.ErrOrStderr()
			child.Env = append(os.Environ(),
				"CLAUDE_CONFIG_DIR="+pr.ConfigDir,
				"CCP_PROFILE="+name,
			)
			if err := child.Run(); err != nil {
				// Propagate the child's exit code. cobra takes care of the
				// nonzero return code mapping.
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					os.Exit(ee.ExitCode())
				}
				// Missing binary, ENOENT on cwd, etc. arrive as *exec.Error.
				// Surface a clear message and let ExitCodeFor classify.
				var pe *exec.Error
				if errors.As(err, &pe) {
					return fmt.Errorf("exec %s: %w", pe.Name, pe.Err)
				}
				return err
			}
			return nil
		},
	}
	return cmd
}
