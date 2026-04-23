package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dalley/ccp/internal/allowlist"
	"github.com/dalley/ccp/internal/paths"
	"github.com/spf13/cobra"
)

// newAllowCmd wires `ccp allow [--status] [--json]`.
//
// Two modes:
//
//   - Default (no --status): walk up from $PWD for `.claude-profile`. When
//     found, hash the marker and upsert the entry into allowlist.toml under
//     the global ccp flock. Prose output names the marker and abbreviated
//     hash. If no marker is found, refuse with a clear hint.
//
//   - --status: walk up from $PWD, classify (Allowed/Unallowed/HashMismatch/
//     NoMarker), and emit either prose or JSON. Read-only; does NOT take the
//     flock — relies on atomic-rename semantics in allowlist.Save. See
//     invariant 3 at the top of internal/allowlist/allowlist.go.
//
// Exit-code discipline:
//
//	Allowed / NoMarker → ExitOK (0)
//	Unallowed / HashMismatch → ExitConflict (4), via the ErrMarkerNotAllowed
//	  / ErrMarkerHashMismatch sentinels the allowlist package returns.
//	Malformed marker (BOM/CRLF/multi-line/...) → ExitUser, via the byte-
//	  offset-annotated ErrInvalidMarker wrap that allowlist.ReadName
//	  produces. The byte-offset context is already in the message; we
//	  surface it verbatim so users see exactly where the file is wrong.
//	Getcwd failure / other I/O → ExitState via the generic mapping.
func newAllowCmd() *cobra.Command {
	var (
		status bool
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "allow",
		Short: "Approve the nearest .claude-profile marker (or check its status with --status)",
		Long: "Walks up from $PWD looking for a .claude-profile file. Without flags, " +
			"records the file's content hash in the per-machine allow-list so " +
			"auto-activation will honour it. With --status, reports whether the " +
			"nearest marker is allowed, unallowed, or has drifted since approval.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if status {
				return runAllowStatus(cmd, asJSON)
			}
			if asJSON {
				// --json is only meaningful with --status. Fail loud
				// rather than silently ignoring it — the caller is
				// likely a script expecting JSON back.
				return fmt.Errorf("--json is only valid with --status")
			}
			return runAllowApprove(cmd)
		},
	}
	cmd.Flags().BoolVar(&status, "status", false, "report the allow-list status for the nearest marker")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON (only with --status)")
	return cmd
}

// newDenyCmd wires `ccp deny`: walk up from $PWD, revoke the entry for the
// nearest `.claude-profile` if one exists. Idempotent — revoking a marker
// that was never approved succeeds silently. Takes the global flock because
// revocation is a state mutation.
func newDenyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "deny",
		Short: "Revoke approval for the nearest .claude-profile marker",
		Long: "Walks up from $PWD looking for a .claude-profile file. If found, " +
			"removes the matching entry from the allow-list. Idempotent: running " +
			"against an already-unapproved marker is a no-op.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDeny(cmd)
		},
	}
}

// findNearestMarker resolves CWD and walks up for a marker. Returns:
//   - (markerAbsPath, cwd, nil) on success
//   - ("", cwd, nil) when no marker was found anywhere up the walk
//   - ("", cwd, err) on getcwd / resolve failure (surfaces as ExitState via
//     the "filesystem" hint heuristic in exit.go)
func findNearestMarker(p paths.Paths) (markerPath, cwd string, err error) {
	cwd, err = os.Getwd()
	if err != nil {
		return "", "", fmt.Errorf("resolve working directory (filesystem): %w", err)
	}
	markerPath, err = allowlist.FindMarker(cwd, p.Home)
	if err != nil {
		return "", cwd, err
	}
	return markerPath, cwd, nil
}

// runAllowApprove implements `ccp allow` (no --status).
func runAllowApprove(cmd *cobra.Command) error {
	s, err := loadState()
	if err != nil {
		return err
	}
	markerPath, cwd, err := findNearestMarker(s.Paths)
	if err != nil {
		return err
	}
	if markerPath == "" {
		return fmt.Errorf("no .claude-profile found in any ancestor of %s "+
			"(walk stops at $HOME or the nearest .git/); create one containing a "+
			"single-line profile name before running `ccp allow`", cwd)
	}

	// Validate the marker name up front. Refuse to approve a file whose
	// contents aren't a valid profile reference — otherwise `ccp allow`
	// happily records a hash for a marker that auto-activation will later
	// refuse to honour, which is confusing. ReadName returns a byte-offset-
	// annotated ErrInvalidMarker for malformed markers.
	name, err := allowlist.ReadName(markerPath)
	if err != nil {
		return err
	}

	// Hash is computed inside Approve under the lock; no need to hash
	// twice. But we want the abbreviated hash for the output message, so
	// hash once outside the lock purely for display, and let Approve
	// hash again authoritatively inside the lock. (A concurrent writer
	// could change the file between the two hashes; we surface whatever
	// Approve recorded, so there's no correctness issue — only a
	// cosmetic "display hash differs from committed hash" window, which
	// would be a benign race against the user's own editor.)
	displayHash, err := allowlist.Hash(markerPath)
	if err != nil {
		return err
	}

	if err := withLock(s.Paths, func() error {
		return allowlist.Approve(s.Paths.AllowlistPath, markerPath)
	}); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Approved %s (profile %q, %s)\n",
		markerPath, name, abbrevHash(displayHash))
	return nil
}

// runAllowStatus implements `ccp allow --status`.
func runAllowStatus(cmd *cobra.Command, asJSON bool) error {
	s, err := loadState()
	if err != nil {
		return err
	}
	markerPath, cwd, err := findNearestMarker(s.Paths)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()

	// No marker anywhere → exit 0 and print "no marker". A fresh CWD is
	// not an error condition.
	if markerPath == "" {
		if asJSON {
			return writeStatusJSON(out, statusReport{
				Marker: "",
				Status: "no_marker",
			})
		}
		fmt.Fprintf(out, "No .claude-profile found in any ancestor of %s\n", cwd)
		return nil
	}

	// Try to resolve the name for reporting. ReadName surfaces byte-offset
	// errors; a malformed marker is a real error, so we bubble it up
	// rather than masking it as "no_marker" — the diagnostic path is
	// supposed to be LOUD.
	name, nameErr := allowlist.ReadName(markerPath)

	// Check is lock-free by design (see allowlist invariant 3).
	status, currentHash, checkErr := allowlist.Check(s.Paths.AllowlistPath, markerPath)

	// If ReadName itself choked and the file is simply malformed, surface
	// that error verbatim — the byte-offset hint is already baked in. The
	// JSON path still emits a structured report for agent consumption.
	if nameErr != nil && errors.Is(nameErr, allowlist.ErrInvalidMarker) {
		if asJSON {
			// Malformed marker: no "profile", no approved_hash. Keep
			// the JSON minimal but present so scripts can branch.
			_ = writeStatusJSON(out, statusReport{
				Marker:      markerPath,
				Status:      "invalid",
				CurrentHash: currentHash,
			})
		}
		return nameErr
	}

	// Non-ErrInvalidMarker ReadName errors (I/O, symlink refusal) are
	// surfaced identically — they're real errors the user should see.
	if nameErr != nil {
		return nameErr
	}

	rep := statusReport{
		Marker:      markerPath,
		Profile:     name,
		CurrentHash: currentHash,
	}

	// Pull the approved hash out of the allowlist for informational
	// display on the drift case. Re-loading is cheap and intentionally
	// done without the lock — drifting hashes are a user-visible debugging
	// signal, and atomic-rename semantics keep it consistent.
	if f, _, lerr := allowlist.Load(s.Paths.AllowlistPath); lerr == nil {
		if abs, aerr := filepath.Abs(markerPath); aerr == nil {
			if h, ok := f.Entries[abs]; ok {
				rep.ApprovedHash = h
			}
		}
	}

	switch status {
	case allowlist.StatusAllowed:
		rep.Status = "allowed"
		if asJSON {
			return writeStatusJSON(out, rep)
		}
		fmt.Fprintf(out, "Allowed %s -> profile %q\n", markerPath, name)
		return nil

	case allowlist.StatusHashMismatch:
		rep.Status = "hash_mismatch"
		if asJSON {
			_ = writeStatusJSON(out, rep)
			// Still return the sentinel so exit code reflects conflict.
			return allowlist.ErrMarkerHashMismatch
		}
		fmt.Fprintf(out, "HashMismatch %s: content changed since approval "+
			"(was %s; now %s); re-run 'ccp allow' after reviewing\n",
			markerPath, abbrevHash(rep.ApprovedHash), abbrevHash(currentHash))
		return allowlist.ErrMarkerHashMismatch

	case allowlist.StatusUnallowed:
		rep.Status = "unallowed"
		if asJSON {
			_ = writeStatusJSON(out, rep)
			return allowlist.ErrMarkerNotAllowed
		}
		fmt.Fprintf(out, "Unallowed %s; run 'ccp allow' to approve\n", markerPath)
		return allowlist.ErrMarkerNotAllowed

	default:
		// A Check error that wasn't one of the two benign sentinels
		// (e.g. allowlist.toml unreadable) — surface it so the user
		// can diagnose. exit.go maps generic I/O errors to ExitState.
		if checkErr != nil {
			return checkErr
		}
		return fmt.Errorf("unexpected status %v for %s", status, markerPath)
	}
}

// runDeny implements `ccp deny`.
func runDeny(cmd *cobra.Command) error {
	s, err := loadState()
	if err != nil {
		return err
	}
	markerPath, cwd, err := findNearestMarker(s.Paths)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if markerPath == "" {
		fmt.Fprintf(out, "No .claude-profile found in any ancestor of %s\n", cwd)
		return nil
	}

	// Look up whether an entry actually exists so we can print a
	// differentiated message ("Revoked X" vs "No entry to revoke for X").
	// This is a lock-free peek; a concurrent writer could change the
	// state between peek and Revoke, but Revoke is idempotent so the end
	// result is the same. Worst case: the message is slightly stale.
	existed := false
	abs, err := filepath.Abs(markerPath)
	if err != nil {
		return err
	}
	if f, _, lerr := allowlist.Load(s.Paths.AllowlistPath); lerr == nil {
		if _, ok := f.Entries[abs]; ok {
			existed = true
		}
	}

	if err := withLock(s.Paths, func() error {
		return allowlist.Revoke(s.Paths.AllowlistPath, markerPath)
	}); err != nil {
		return err
	}

	if existed {
		fmt.Fprintf(out, "Revoked %s\n", markerPath)
	} else {
		fmt.Fprintf(out, "No entry to revoke for %s\n", markerPath)
	}
	return nil
}

// statusReport is the --json payload shape for `ccp allow --status`.
// Field order/names are load-bearing — agent consumers parse these.
type statusReport struct {
	Marker       string `json:"marker"`
	Status       string `json:"status"`
	Profile      string `json:"profile,omitempty"`
	CurrentHash  string `json:"current_hash,omitempty"`
	ApprovedHash string `json:"approved_hash,omitempty"`
}

func writeStatusJSON(out io.Writer, r statusReport) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// abbrevHash shortens a "sha256:<hex>" value to "sha256:<first12>" for
// display. Falls back to the original string for non-canonical input.
func abbrevHash(h string) string {
	const prefix = "sha256:"
	if len(h) <= len(prefix)+12 {
		return h
	}
	return h[:len(prefix)+12]
}
