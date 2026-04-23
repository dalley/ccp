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

// writePosix emits the POSIX (bash/zsh) shell-init snippet.
//
// Two logical layers:
//
//   1. Legacy activation (`__ccp_activate_legacy`): the v1 behavior — exports
//      CLAUDE_CONFIG_DIR from $CCP_PROFILE or `ccp shell-active`. Unchanged
//      semantics; still invoked as the fallback when auto-activation is off
//      or yields no result.
//
//   2. Auto-activation (`__ccp_activate`): walks up from $PWD for a
//      `.claude-profile` marker in pure shell, consults a per-shell env-var
//      cache, and only forks `ccp shell-resolve-dir` on a cache miss. The
//      resolver emits shellQuote-wrapped KEY=value lines we `eval`. On
//      `CCP_AUTO_WARN` (drift/unallowed) we emit a one-shot stderr warning
//      guarded by a per-marker env var so each shell session warns at most
//      once per marker.
//
// The walk-up is bounded at 64 ancestors (depth defense against a pathological
// symlink loop or a crafted deep tree). We never mutate the user's $PWD during
// the walk — we use a local `_ccp_d` variable and call `dirname`.
//
// Zero-fork cache-hit path: when $PWD's nearest marker matches $CCP_AUTO_MARKER
// by path AND mtime, we reuse $CCP_AUTO_PROFILE without ever calling `ccp`.
// Similarly, if $PWD is an ancestor of (or equal to) $CCP_AUTO_NOMARKER_ROOT,
// we know no marker exists above us and skip the whole pipeline.
//
// The per-shell chpwd hook is registered after __ccp_activate is defined:
//   - zsh: `add-zsh-hook chpwd __ccp_activate`
//   - bash: `PROMPT_COMMAND="__ccp_activate;${PROMPT_COMMAND}"`
// and __ccp_activate is also called once inline to handle the startup shell.
//
// CLAUDE_CONFIG_DIR escape-hatch semantics match v1: if it was set by anything
// outside this hook, we leave it alone. Auto-activation only runs when it is
// unset at hook entry, so once the auto layer has exported it for a session
// the value is sticky until the user unsets it (or sets CCP_PROFILE_AUTO=0 and
// opens a new shell). That matches decision #14's design — the shell layer is
// advisory, not a continuous reconciler.
func writePosix(w io.Writer, _ paths.Paths) {
	_, _ = io.WriteString(w, `__ccp_activate_legacy() {
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
__ccp_activate() {
  [ -n "$CLAUDE_CONFIG_DIR" ] && return 0
  if [ "$CCP_PROFILE_AUTO" = "0" ]; then
    __ccp_activate_legacy
    return
  fi
  # Walk up from $PWD for .claude-profile, bounded by 64 ancestors.
  # Do not mutate the user's $PWD.
  local _ccp_d="$PWD"
  local _ccp_marker=""
  local _ccp_i=0
  while [ "$_ccp_i" -lt 64 ]; do
    if [ -f "$_ccp_d/.claude-profile" ]; then
      _ccp_marker="$_ccp_d/.claude-profile"
      break
    fi
    case "$_ccp_d" in
      /|'') break ;;
    esac
    _ccp_d=$(dirname -- "$_ccp_d" 2>/dev/null) || break
    _ccp_i=$((_ccp_i + 1))
  done
  if [ -z "$_ccp_marker" ]; then
    # Negative cache: only a hit if $PWD is the cached no-marker root or an
    # ancestor of it. Sibling paths must re-walk (a new marker could exist
    # under them).
    if [ -n "$CCP_AUTO_NOMARKER_ROOT" ]; then
      case "$CCP_AUTO_NOMARKER_ROOT/" in
        "$PWD"/*|"$PWD"/) __ccp_activate_legacy; return ;;
      esac
    fi
    export CCP_AUTO_NOMARKER_ROOT="$PWD"
    unset CCP_AUTO_MARKER CCP_AUTO_MARKER_MTIME CCP_AUTO_PROFILE
    __ccp_activate_legacy
    return
  fi
  # Found a marker. Capture mtime in pure shell (no ccp fork yet).
  local _ccp_mtime=""
  if [ -n "$ZSH_VERSION" ]; then
    # zsh: use stat builtin if available via zstat module.
    zmodload -e zsh/stat 2>/dev/null && _ccp_mtime=$(zstat +mtime -- "$_ccp_marker" 2>/dev/null)
  fi
  if [ -z "$_ccp_mtime" ]; then
    # POSIX-ish fallback: GNU coreutils stat, BSD stat, or perl.
    _ccp_mtime=$(stat -c %Y -- "$_ccp_marker" 2>/dev/null) \
      || _ccp_mtime=$(stat -f %m -- "$_ccp_marker" 2>/dev/null) \
      || _ccp_mtime=""
  fi
  # Cache hit: same marker path AND same mtime → skip the fork entirely.
  if [ -n "$CCP_AUTO_MARKER" ] \
     && [ "$CCP_AUTO_MARKER" = "$_ccp_marker" ] \
     && [ -n "$CCP_AUTO_MARKER_MTIME" ] \
     && [ -n "$_ccp_mtime" ] \
     && [ "$CCP_AUTO_MARKER_MTIME" = "$_ccp_mtime" ] \
     && [ -n "$CCP_AUTO_PROFILE" ]; then
    export CCP_PROFILE="$CCP_AUTO_PROFILE"
    __ccp_activate_legacy
    return
  fi
  # Cache miss. Invalidate negative cache (we found a marker above $PWD).
  unset CCP_AUTO_NOMARKER_ROOT
  # Clear stale cache vars; the eval below repopulates them.
  unset CCP_AUTO_MARKER CCP_AUTO_MARKER_MTIME CCP_AUTO_PROFILE CCP_AUTO_WARN
  if command -v ccp >/dev/null 2>&1; then
    eval "$(ccp shell-resolve-dir "$_ccp_d" 2>/dev/null)"
  fi
  # Warn once per marker on drift/unallowed.
  if [ -n "$CCP_AUTO_WARN" ]; then
    # Per-marker guard key: strip non-alphanumeric chars from marker path.
    local _ccp_key
    _ccp_key=$(printf '%s' "$_ccp_marker" | tr -cd 'A-Za-z0-9')
    local _ccp_guard="CCP_AUTO_WARNED_${_ccp_key}"
    # Indirect expansion (POSIX): use eval to read the guard var.
    local _ccp_seen=""
    eval "_ccp_seen=\${$_ccp_guard:-}"
    if [ -z "$_ccp_seen" ]; then
      printf 'ccp: %s at %s -- run: ccp allow --status\n' \
        "$CCP_AUTO_WARN" "$_ccp_marker" >&2
      eval "export $_ccp_guard=1"
    fi
    unset CCP_AUTO_WARN
  fi
  if [ -n "$CCP_AUTO_PROFILE" ]; then
    export CCP_PROFILE="$CCP_AUTO_PROFILE"
  fi
  __ccp_activate_legacy
}
# Register on-cd hooks so auto-activation fires on every directory change.
if [ -n "$ZSH_VERSION" ]; then
  autoload -Uz add-zsh-hook 2>/dev/null && add-zsh-hook chpwd __ccp_activate
elif [ -n "$BASH_VERSION" ]; then
  case ";${PROMPT_COMMAND:-};" in
    *";__ccp_activate;"*) : ;;
    *) PROMPT_COMMAND="__ccp_activate;${PROMPT_COMMAND}" ;;
  esac
fi
__ccp_activate
`)
}

// writeFish emits the fish variant of the shell-init snippet.
//
// Mirrors the POSIX two-function structure: __ccp_activate_legacy for the v1
// CCP_PROFILE / shell-active path, __ccp_activate for the auto-activation
// walk with env-var cache and warn-once guard. Uses `--on-variable PWD` to
// fire on every directory change.
func writeFish(w io.Writer, _ paths.Paths) {
	_, _ = io.WriteString(w, `function __ccp_activate_legacy
  if set -q CLAUDE_CONFIG_DIR
    return 0
  end
  set -l profile ""
  if set -q CCP_PROFILE
    set profile $CCP_PROFILE
  else if command -q ccp
    set profile (ccp shell-active 2>/dev/null)
  end
  if not string match -rq '^[a-z][a-z0-9_-]*$' -- "$profile"
    return 0
  end
  set -gx CLAUDE_CONFIG_DIR "$HOME/.claude-$profile"
end
function __ccp_activate
  if set -q CLAUDE_CONFIG_DIR
    return 0
  end
  if set -q CCP_PROFILE_AUTO; and test "$CCP_PROFILE_AUTO" = "0"
    __ccp_activate_legacy
    return
  end
  set -l d $PWD
  set -l marker ""
  set -l i 0
  while test $i -lt 64
    if test -f "$d/.claude-profile"
      set marker "$d/.claude-profile"
      break
    end
    if test "$d" = "/"; or test -z "$d"
      break
    end
    set d (dirname -- "$d" 2>/dev/null); or break
    set i (math $i + 1)
  end
  if test -z "$marker"
    if set -q CCP_AUTO_NOMARKER_ROOT; and test -n "$CCP_AUTO_NOMARKER_ROOT"
      if test "$CCP_AUTO_NOMARKER_ROOT" = "$PWD"; or string match -q "$PWD/*" -- "$CCP_AUTO_NOMARKER_ROOT"
        __ccp_activate_legacy
        return
      end
    end
    set -gx CCP_AUTO_NOMARKER_ROOT "$PWD"
    set -e CCP_AUTO_MARKER
    set -e CCP_AUTO_MARKER_MTIME
    set -e CCP_AUTO_PROFILE
    __ccp_activate_legacy
    return
  end
  set -l mtime (stat -c %Y -- "$marker" 2>/dev/null; or stat -f %m -- "$marker" 2>/dev/null)
  if set -q CCP_AUTO_MARKER; and test -n "$CCP_AUTO_MARKER" \
     -a "$CCP_AUTO_MARKER" = "$marker" \
     -a -n "$CCP_AUTO_MARKER_MTIME" \
     -a -n "$mtime" \
     -a "$CCP_AUTO_MARKER_MTIME" = "$mtime" \
     -a -n "$CCP_AUTO_PROFILE"
    set -gx CCP_PROFILE $CCP_AUTO_PROFILE
    __ccp_activate_legacy
    return
  end
  set -e CCP_AUTO_NOMARKER_ROOT
  set -e CCP_AUTO_MARKER
  set -e CCP_AUTO_MARKER_MTIME
  set -e CCP_AUTO_PROFILE
  set -e CCP_AUTO_WARN
  if command -q ccp
    eval (ccp shell-resolve-dir "$d" 2>/dev/null | string collect)
  end
  if set -q CCP_AUTO_WARN; and test -n "$CCP_AUTO_WARN"
    set -l key (string replace -ra '[^A-Za-z0-9]' '' -- "$marker")
    set -l guard "CCP_AUTO_WARNED_$key"
    if not set -q $guard
      printf 'ccp: %s at %s -- run: ccp allow --status\n' $CCP_AUTO_WARN $marker >&2
      set -gx $guard 1
    end
    set -e CCP_AUTO_WARN
  end
  if set -q CCP_AUTO_PROFILE; and test -n "$CCP_AUTO_PROFILE"
    set -gx CCP_PROFILE $CCP_AUTO_PROFILE
  end
  __ccp_activate_legacy
end
function __ccp_activate_pwd --on-variable PWD
  __ccp_activate
end
__ccp_activate
`)
}
