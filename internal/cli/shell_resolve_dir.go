package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dalley/ccp/internal/allowlist"
	"github.com/dalley/ccp/internal/paths"
	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

// newShellResolveDirCmd returns the hidden `ccp shell-resolve-dir <dir>`
// command. This is a hot-path helper called on every `cd` by the
// auto-activation shell hook. It walks up from <dir> looking for a
// `.claude-profile` marker, checks it against the per-machine allow-list
// (~/.config/ccp/allowlist.toml), and emits shell-evalable KEY='value'
// lines the hook then consumes via `eval`.
//
// ---------------------------------------------------------------------------
// Invariants (load-bearing; do NOT weaken without touching the v2.0 plan):
// ---------------------------------------------------------------------------
//
//   1. Never errors on the hot path. EVERY failure path — unresolvable
//      home, missing dir, missing allowlist, unreadable allowlist, malformed
//      marker, symlinked marker, invalid profile name, hash mismatch on I/O
//      layer — returns nil (exit 0) with empty or minimal stdout. The shell
//      hook cannot safely surface a stderr message without corrupting
//      prompts in every shell on every cd. Errors are diagnosed via
//      `ccp allow --status`, not here. Mirrors internal/cli/shellactive.go.
//
//   2. Never creates directories. Uses paths.Resolve(), NOT p.Ensure(). The
//      shell hook fires on cd events in shells the user may have opened in
//      read-only contexts (a docker bind mount, a recovery shell, etc.);
//      mkdir on every cd is both presumptuous and a correctness hazard.
//
//   3. Lock-free read. allowlist.Check reads the allowlist without taking
//      the global ccp flock. Atomic rename-on-save in allowlist.Save
//      guarantees a reader sees either the complete old state or the
//      complete new state — never torn. Taking the flock on every cd would
//      serialize every prompt in every shell; this design is load-bearing
//      for latency. Codified as invariant 3 in internal/allowlist/allowlist.go.
//
//   4. Symlinked markers silently skip. allowlist.Hash opens with
//      O_NOFOLLOW and returns ELOOP when the marker itself is a symlink;
//      we swallow that identically to a malformed marker — empty stdout,
//      exit 0, NO CCP_AUTO_WARN line. This is deliberate: emitting a
//      warning for the symlink case would create an existence oracle — an
//      attacker probing whether a user has a specific path in their
//      filesystem could distinguish "no marker" from "symlinked marker"
//      and learn that the target exists. Do NOT add a warning here in the
//      future. If this surfaces a real UX problem (user wonders why
//      auto-activation isn't firing), the diagnostic path is
//      `ccp allow --status` or `ccp allow <dir>`, which DO surface errors.
//
//   5. All user-controlled values pass through shellQuote. Profile names
//      are already regex-validated by allowlist.ReadName and re-validated
//      here with profile.ValidateName (belt & suspenders), but we still
//      shellQuote them. CCP_AUTO_WARN values are hardcoded constants
//      (drift, unallowed); we quote them too for consistency.
//
// ---------------------------------------------------------------------------
// Output format:
// ---------------------------------------------------------------------------
//
//   Allowed match — three lines, shell-quoted, separated by '\n':
//       CCP_AUTO_PROFILE='<name>'
//       CCP_AUTO_MARKER='<abs-path>'
//       CCP_AUTO_MARKER_MTIME='<unix-seconds>'
//
//   Drift (entry exists, hash mismatch):
//       CCP_AUTO_WARN='drift'
//
//   Unallowed (no entry for this marker's path):
//       CCP_AUTO_WARN='unallowed'
//
//   No marker / any error / symlinked marker / malformed marker:
//       (empty stdout; exit 0)
func newShellResolveDirCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "shell-resolve-dir <dir>",
		Short:  "Resolve the .claude-profile marker for <dir> (hidden; used by shell-init)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]
			if dir == "" {
				return nil
			}
			// paths.Resolve is cheap and does no filesystem mutation. If it
			// fails (no HOME, CCP_ROOT unresolvable), we silently exit 0.
			p, err := paths.Resolve()
			if err != nil {
				return nil
			}
			// If <dir> doesn't exist, silently skip. FindMarker calls
			// filepath.Abs which will succeed even for nonexistent paths,
			// but Lstat inside it would fail — and we don't want to start
			// walking an imaginary ancestor chain either.
			if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
				return nil
			}

			markerPath, err := allowlist.FindMarker(dir, p.Home)
			if err != nil || markerPath == "" {
				return nil
			}

			// Re-validate the profile name inline. allowlist.ReadName
			// already validates against the marker grammar, but this is
			// defense in depth — if the file changes between ReadName and
			// here (it doesn't in our implementation, but future refactors
			// might), we still refuse to emit an unvalidated name into a
			// shell eval context.
			name, err := allowlist.ReadName(markerPath)
			if err != nil {
				// Malformed marker, symlinked marker (ELOOP wrapped in the
				// "refused to follow symlink" error), unreadable file —
				// all fail closed with empty stdout.
				return nil
			}
			if err := profile.ValidateName(name); err != nil {
				return nil
			}

			// Check the allow-list. Hash(markerPath) is called inside
			// Check — a symlinked marker would ELOOP here too, which we
			// map to "silent skip, no warning" per invariant 4. The
			// ReadName call above would typically catch it first, but we
			// still have to handle it here for robustness against race
			// conditions where the marker becomes a symlink between
			// ReadName and Check.
			//
			// Check's return discipline:
			//   (StatusAllowed,       nil)                      — emit 3-line output
			//   (StatusHashMismatch,  ErrMarkerHashMismatch)    — emit CCP_AUTO_WARN=drift
			//   (StatusUnallowed,     ErrMarkerNotAllowed)      — emit CCP_AUTO_WARN=unallowed
			//   (StatusUnallowed,     <any other err>)          — silent skip (I/O failure,
			//                                                     allowlist unreadable,
			//                                                     Hash error). We must not
			//                                                     conflate these with the
			//                                                     canonical "unallowed"
			//                                                     case; that would leak
			//                                                     misleading signal to the
			//                                                     hook.
			status, _, checkErr := allowlist.Check(p.AllowlistPath, markerPath)
			if checkErr != nil && !errorIsBenignAllowlistSignal(checkErr) {
				return nil
			}

			out := cmd.OutOrStdout()
			switch status {
			case allowlist.StatusAllowed:
				abs, err := filepath.Abs(markerPath)
				if err != nil {
					return nil
				}
				info, err := os.Lstat(abs)
				if err != nil {
					return nil
				}
				// Three-line emission; every value shellQuote-wrapped.
				_, _ = fmt.Fprintf(out, "CCP_AUTO_PROFILE=%s\n", shellQuote(name))
				_, _ = fmt.Fprintf(out, "CCP_AUTO_MARKER=%s\n", shellQuote(abs))
				_, _ = fmt.Fprintf(out, "CCP_AUTO_MARKER_MTIME=%s\n",
					shellQuote(fmt.Sprintf("%d", info.ModTime().Unix())))
			case allowlist.StatusHashMismatch:
				_, _ = fmt.Fprintf(out, "CCP_AUTO_WARN=%s\n", shellQuote("drift"))
			case allowlist.StatusUnallowed:
				_, _ = fmt.Fprintf(out, "CCP_AUTO_WARN=%s\n", shellQuote("unallowed"))
			default:
				// Any error from Check (unreadable allowlist, I/O hiccup
				// hashing the marker) lands here with StatusUnallowed's
				// zero value — but Check may also return on an error
				// path. In the hot path we treat "anything we couldn't
				// decide" as silent skip.
			}
			return nil
		},
	}
}

// errorIsBenignAllowlistSignal reports whether err from allowlist.Check
// is one of the two sentinel errors that are load-bearing signals (the
// entry is missing, or the hash drifted) vs a genuine I/O failure that
// the hot path should silently skip over.
//
// Kept as a named helper (not inline) so the intent is obvious at the
// call site: "is this an error we want to surface as CCP_AUTO_WARN, or
// is this an error that means 'pretend no marker exists'?"
func errorIsBenignAllowlistSignal(err error) bool {
	return errors.Is(err, allowlist.ErrMarkerNotAllowed) ||
		errors.Is(err, allowlist.ErrMarkerHashMismatch)
}

// shellQuote wraps s in POSIX-safe single quotes, escaping any embedded
// single quote via the conventional `'\''` sequence (close, literal quote,
// reopen). Handles every byte safely — spaces, $, backticks, newlines,
// backslashes all pass through inside the single-quoted span.
//
// Kept inline next to the command that uses it so reviewers see the
// quoting discipline alongside every emission site.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
