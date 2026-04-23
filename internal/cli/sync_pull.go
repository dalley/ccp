package cli

import (
	"fmt"

	"github.com/dalley/ccp/internal/backup"
	ccpsync "github.com/dalley/ccp/internal/sync"
	"github.com/spf13/cobra"
)

func newSyncPullCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Fetch and merge changes from origin (non-destructive by default)",
		Long: `Pull fetches and merges changes from origin. Non-destructive by default:
if the working tree has uncommitted changes, it refuses and asks you to
either push them first or re-run with --force.

--force backs up profiles/ into ~/.config/ccp/backups/ before discarding
local changes.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := loadState()
			if err != nil {
				return err
			}
			if ok, _ := ccpsync.IsSyncRepo(s.Paths.ConfigDir); !ok {
				return fmt.Errorf("sync not set up — run `ccp sync setup` first")
			}

			return withLock(s.Paths, func() error {
				var bk string
				if force {
					dir, err := backup.New(s.Paths.BackupsDir, "pre-pull")
					if err != nil {
						return err
					}
					bk = dir
				}
				changed, err := ccpsync.Pull(s.Paths.ConfigDir, ccpsync.PullOptions{
					Force:     force,
					BackupDir: bk,
				})
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				if force {
					fmt.Fprintf(out, "Pre-pull backup: %s\n", s.Paths.ToHomeRelative(bk))
				}
				if changed {
					fmt.Fprintln(out, "Pulled new changes. Run `ccp profile refresh` to update symlinks.")
				} else {
					fmt.Fprintln(out, "Already up to date.")
				}
				return backup.Prune(s.Paths.BackupsDir, backup.DefaultRetention)
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "discard local changes (backed up first)")
	return cmd
}
