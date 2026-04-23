package cli

import (
	"fmt"

	ccpsync "github.com/dalley/ccp/internal/sync"
	"github.com/spf13/cobra"
)

func newSyncPushCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Commit changes and push to origin",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := loadState()
			if err != nil {
				return err
			}
			if ok, _ := ccpsync.IsSyncRepo(s.Paths.ConfigDir); !ok {
				return fmt.Errorf("sync not set up — run `ccp sync setup --url <git>` first")
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
				if err := ccpsync.Push(s.Paths.ConfigDir, ccpsync.PushOptions{DryRun: dryRun}); err != nil {
					return fmt.Errorf("push: %w", err)
				}
				fmt.Fprintln(out, "Pushed to", remote)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would happen without pushing")
	return cmd
}
