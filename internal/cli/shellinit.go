package cli

import (
	"fmt"
	"io"

	"github.com/dalley/ccp/internal/manifest"
	"github.com/dalley/ccp/internal/paths"
	"github.com/spf13/cobra"
)

// Marker lines on the emitted snippet. Keep these stable — users may grep for
// them and a future `ccp shell-init --uninstall` will key off them.
const (
	shellInitBegin = "# >>> ccp shell-init >>>"
	shellInitEnd   = "# <<< ccp shell-init <<<"
)

func newShellInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "shell-init [shell]",
		Short:     "Print shell snippet to eval from your shellrc",
		ValidArgs: []string{"zsh", "bash", "fish"},
		Args:      cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := paths.Resolve()
			if err != nil {
				return err
			}
			shell := ""
			if len(args) == 1 {
				shell = args[0]
			} else if m, _, err := manifest.Load(p.ManifestPath); err == nil {
				shell = m.DefaultShell
			}
			if shell == "" {
				shell = "zsh"
			}

			return writeShellInit(cmd.OutOrStdout(), shell, p)
		},
	}
}

func writeShellInit(w io.Writer, shell string, p paths.Paths) error {
	fmt.Fprintln(w, shellInitBegin)
	switch shell {
	case "fish":
		writeFish(w, p)
	case "bash", "zsh":
		writePosix(w, p)
	default:
		return fmt.Errorf("unsupported shell %q (supported: zsh, bash, fish)", shell)
	}
	fmt.Fprintln(w, shellInitEnd)
	return nil
}

// writePosix emits a snippet that reads the active profile from the manifest
// on shell startup and, if set, exports CLAUDE_CONFIG_DIR accordingly — unless
// CLAUDE_CONFIG_DIR is already set in the environment (escape hatch) or
// CCP_PROFILE is set (per-shell override). The snippet uses $HOME so it is
// portable across machines with different usernames.
//
// Delegates TOML parsing to `ccp shell-active` instead of awk so schema
// evolution in manifest.toml (comments, multi-line values, BOMs) can't
// silently break activation. The name is also re-validated inline as a
// defence-in-depth against any future bug in shell-active's output.
func writePosix(w io.Writer, _ paths.Paths) {
	fmt.Fprint(w, `__ccp_activate() {
  [ -n "$CLAUDE_CONFIG_DIR" ] && return 0
  local profile=""
  if [ -n "$CCP_PROFILE" ]; then
    profile="$CCP_PROFILE"
  elif command -v ccp >/dev/null 2>&1; then
    profile="$(ccp shell-active 2>/dev/null)"
  fi
  case "$profile" in
    ''|*[!a-z0-9_-]*|[!a-z]*) return 0 ;;
  esac
  export CLAUDE_CONFIG_DIR="$HOME/.claude-$profile"
}
__ccp_activate
`)
}

func writeFish(w io.Writer, _ paths.Paths) {
	fmt.Fprint(w, `function __ccp_activate
  if set -q CLAUDE_CONFIG_DIR
    return 0
  end
  set -l profile ""
  if set -q CCP_PROFILE
    set profile $CCP_PROFILE
  else if command -q ccp
    set profile (ccp shell-active 2>/dev/null)
  end
  # Re-validate inline as defence in depth.
  if not string match -rq '^[a-z][a-z0-9_-]*$' -- "$profile"
    return 0
  end
  set -gx CLAUDE_CONFIG_DIR "$HOME/.claude-$profile"
end
__ccp_activate
`)
}
