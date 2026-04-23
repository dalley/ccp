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
			latest, err := backup.Latest(s.Paths.BackupsDir)
			if err != nil {
				return err
			}
			if latest == "" {
				return fmt.Errorf("no backups to restore from")
			}
			var restored []string
			err = withLock(s.Paths, func() error {
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
