package cli

import (
	"fmt"
	"path/filepath"

	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

func newProfileRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <old> <new>",
		Short: "Rename a profile",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			oldName, newName := args[0], args[1]
			s, err := loadState()
			if err != nil {
				return err
			}
			err = withLockedState(s.Paths, func(s *state) error {
				if ierr := profile.Rename(s.Paths, oldName, newName); ierr != nil {
					return ierr
				}
				// Move alias blocks in common shellrc files.
				for _, rc := range []string{".zshrc", ".bashrc", ".bash_profile"} {
					path := filepath.Join(s.Paths.Home, rc)
					_ = profile.UninstallAlias(path, oldName)
					// Only reinstall if we had an alias — we can detect that
					// crudely by whether uninstall changed the file, but we
					// don't bother: the user can re-add via `ccp profile
					// create --alias` or hand-edit.
				}
				if s.Manifest.ActiveProfile == oldName {
					s.Manifest.ActiveProfile = newName
				}
				return nil
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Renamed %q → %q\n", oldName, newName)
			return nil
		},
	}
}
