package cli

import (
	"fmt"

	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

func newProfileRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh [name]",
		Short: "Rebuild runtime symlinks from the profile source tree",
		Long: `Refresh rebuilds the profile's runtime directory symlinks from the
current state of the source tree. Use after manually editing files in
~/.config/ccp/profiles/<name>/ or after pulling new content via sync.
With no argument, refreshes every profile.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := loadState()
			if err != nil {
				return err
			}
			var targets []profile.Profile
			if len(args) == 1 {
				pr, err := profile.NewChecked(s.Paths, args[0])
				if err != nil {
					return err
				}
				if !pr.Exists() {
					return fmt.Errorf("profile %q not found", args[0])
				}
				targets = []profile.Profile{pr}
			} else {
				got, err := profile.List(s.Paths)
				if err != nil {
					return err
				}
				targets = got
			}

			return withLock(s.Paths, func() error {
				for _, pr := range targets {
					if err := pr.RefreshSymlinks(); err != nil {
						return fmt.Errorf("refresh %s: %w", pr.Name, err)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Refreshed %s\n", pr.Name)
				}
				return nil
			})
		},
	}
}
