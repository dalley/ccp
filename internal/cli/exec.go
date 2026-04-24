package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"time"

	"github.com/dalley/ccp/internal/profile"
	"github.com/dalley/ccp/internal/refs"
	"github.com/spf13/cobra"
)

// ExecRefreshTimeout is the default ceiling on secret resolution inside
// the `ccp exec` refresh step. 30s is enough for op-CLI round trips on a
// slow link; past that, something is wrong and we prefer to surface a
// clear error over wedging the shell.
//
// The same value is reused by `ccp profile refresh`, `ccp profile create`,
// and `ccp profile rename` (which all call BuildSymlinks / RefreshSymlinks
// internally) so a slow or hung `op read` can't pin the user's terminal
// indefinitely. See the Ctx variants on profile.Profile / profile.Create
// / profile.Rename for the plumbing.
//
// Declared as a var (not const) so tests can shrink the budget to keep
// the "timeout exercised on slow op read" case fast.
var ExecRefreshTimeout = 30 * time.Second

// execRefreshCalls is a test-observable counter of how many times the
// exec command has actually run RefreshSymlinks. Used by integration
// tests that need to verify the "skip refresh when no refs" fast path.
// Callers read it via LoadExecRefreshCount.
var execRefreshCalls atomic.Int64

// LoadExecRefreshCount returns the current count of exec-triggered
// refresh invocations. Exported for tests.
func LoadExecRefreshCount() int64 { return execRefreshCalls.Load() }

// ResetExecRefreshCount zeros the counter (tests share the package-level
// state; each test resets before asserting).
func ResetExecRefreshCount() { execRefreshCalls.Store(0) }

func newExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec <profile> -- <command...>",
		Short: "Run a command with CLAUDE_CONFIG_DIR set to the given profile",
		Long: `exec runs <command...> with CLAUDE_CONFIG_DIR pointing at <profile>'s
runtime directory, without changing the global active profile. Useful for
scripts, CI, or running a one-off command against a non-active profile.

If the profile contains any {{ ... }} secret references, ccp refreshes
the runtime directory (re-resolving refs) before starting the child.
Pass --no-refresh (or set CCP_EXEC_NO_REFRESH=1) to skip the refresh
for a fast path at the cost of potentially-stale rendered content.

Use -- to separate the profile name from the command arguments. When
using --no-refresh, put the flag BEFORE the profile name so it isn't
forwarded to the child process.

Examples:
  ccp exec work -- claude --help
  ccp exec --no-refresh work -- /bin/true
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
			// DisableFlagParsing forwards every token untouched so child
			// args like `claude --help` aren't parsed by cobra. That
			// means we have to pluck `--no-refresh` ourselves. Contract:
			// the flag is ONLY recognized in the leading position — if
			// the profile name isn't first, nothing changes. This keeps
			// "everything after the profile name is the child's" clean.
			noRefresh := false
			if os.Getenv("CCP_EXEC_NO_REFRESH") == "1" {
				noRefresh = true
			}
			if len(args) > 0 && args[0] == "--no-refresh" {
				noRefresh = true
				args = args[1:]
			}
			if len(args) < 2 {
				return fmt.Errorf("usage: ccp exec [--no-refresh] <profile> -- <command...>")
			}

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

			if !noRefresh {
				// HasAnyRefs short-circuits: profiles without refs pay
				// zero refresh cost. Errors reading the source tree
				// bubble up unchanged — if we can't tell whether refs
				// exist, we can't safely run the child with stale
				// rendered content either.
				hasRefs, err := refs.HasAnyRefs(pr.SourceDir)
				if err != nil && !errors.Is(err, refs.ErrUnsupportedPlatform) {
					return fmt.Errorf("scan source for refs: %w", err)
				}
				if hasRefs {
					ctx, cancel := context.WithTimeout(cmd.Context(), ExecRefreshTimeout)
					defer cancel()
					execRefreshCalls.Add(1)
					if err := withLock(s.Paths, func() error {
						return pr.RefreshSymlinksCtx(ctx)
					}); err != nil {
						return fmt.Errorf("refresh %s before exec: %w", name, err)
					}
				}
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
				// Propagate the child's exit code. POSIX convention:
				// signal-killed children return 128+signum so shells can
				// distinguish e.g. SIGSEGV (139) from SIGKILL (137) from
				// a plain non-zero exit. exec.ExitError.ExitCode() returns
				// -1 for signal death, which os.Exit would truncate to 255
				// — colliding with SSH transport-error convention.
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					code := ee.ExitCode()
					if code < 0 {
						code = signalExitCode(ee)
					}
					os.Exit(code)
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
