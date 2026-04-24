package cli

import (
	"errors"
	"fmt"

	"github.com/dalley/ccp/internal/profile"
	ccpsync "github.com/dalley/ccp/internal/sync"
	"github.com/spf13/cobra"
)

func newSyncPushCmd() *cobra.Command {
	var dryRun bool
	var quiet bool
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Commit changes and push to origin",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := loadState()
			if err != nil {
				return err
			}
			if ok, _ := ccpsync.IsSyncRepo(s.Paths.ConfigDir); !ok {
				return errors.New("sync not set up — run `ccp sync setup --url <git>` first")
			}

			// Advisory audit on the active profile BEFORE push so the user
			// sees the warning in the same output where they'd notice
			// cleartext secrets about to travel to a git remote. This is a
			// soft warning only — the push proceeds either way. If the user
			// is intentionally syncing a profile with ref placeholders
			// (which render as cleartext hits), `--quiet` silences the noise.
			//
			// We count via profile.CountReal so informational skip entries
			// (skipped-large / skipped-binary) don't trigger the advisory —
			// an oversized vendored blob in the profile tree shouldn't
			// scare the user into thinking a secret is about to leak.
			if !quiet && s.Manifest.ActiveProfile != "" {
				if findings, aerr := profile.Audit(s.Paths, s.Manifest.ActiveProfile); aerr == nil {
					if n := profile.CountReal(findings); n > 0 {
						fmt.Fprintf(cmd.ErrOrStderr(),
							"ccp: %d suspected secrets detected in profile %s; review with 'ccp profile audit'\n",
							n, s.Manifest.ActiveProfile)
					}
				}
			}

			return withLock(s.Paths, func() error {
				out := cmd.OutOrStdout()
				committed, err := ccpsync.StageAndCommit(s.Paths.ConfigDir)
				if err != nil {
					return err
				}
				switch {
				case committed:
					fmt.Fprintln(out, "Committed local changes.")
				case dryRun:
					fmt.Fprintln(out, "No local changes to commit (dry-run).")
					return nil
				default:
					fmt.Fprintln(out, "Nothing to commit.")
				}

				if dryRun {
					fmt.Fprintln(out, "Dry-run: would push origin.")
					return nil
				}

				remote, _ := ccpsync.Remote(s.Paths.ConfigDir)
				if remote == "" {
					fmt.Fprintln(out, "No remote configured; skipping push.")
					return nil
				}
				if err := ccpsync.Push(s.Paths.ConfigDir); err != nil {
					return fmt.Errorf("push: %w", err)
				}
				fmt.Fprintln(out, "Pushed to", remote)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would happen without pushing")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress the pre-push secret-audit advisory")
	return cmd
}
