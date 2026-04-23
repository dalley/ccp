package cli

import (
	"fmt"

	"github.com/dalley/ccp/internal/backup"
	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

func newProfileRollbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rollback",
		Short: "Restore the most recent backup (undo the last delete)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := loadState()
			if err != nil {
				return err
			}
			var (
				restored []string
				latest   string
			)
			err = withLock(s.Paths, func() error {
				// Compute Latest INSIDE the lock. Otherwise a concurrent
				// `ccp profile delete` + Prune could delete the dir we
				// just resolved, and Rollback would fail with a confusing
				// filesystem error rather than a clear "nothing to restore".
				l, ierr := backup.Latest(s.Paths.BackupsDir)
				if ierr != nil {
					return ierr
				}
				if l == "" {
					return fmt.Errorf("no backups to restore from")
				}
				latest = l
				r, ierr := profile.Rollback(s.Paths, latest)
				if ierr != nil {
					return ierr
				}
				restored = r
				return nil
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Restored %d profile(s) from %s:\n",
				len(restored), s.Paths.ToHomeRelative(latest))
			for _, n := range restored {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", n)
			}
			return nil
		},
	}
}
