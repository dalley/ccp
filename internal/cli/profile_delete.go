package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dalley/ccp/internal/backup"
	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newProfileDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:               "delete <name>",
		Short:             "Delete a profile (source + runtime moved to backup)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProfileName,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			s, err := loadState()
			if err != nil {
				return err
			}
			pr, err := profile.NewChecked(s.Paths, name)
			if err != nil {
				return err
			}
			if !pr.Exists() {
				return fmt.Errorf("profile %q not found", name)
			}
			if !yes {
				// Refuse to prompt when stdin isn't a TTY — an agent piping
				// us /dev/null would otherwise see the prompt read EOF,
				// silently return false, exit 0 with "Cancelled.", and have
				// no way to tell what happened. Force the caller to pass
				// --yes instead.
				if !term.IsTerminal(int(os.Stdin.Fd())) {
					return fmt.Errorf("refusing to prompt for confirmation on a non-interactive stdin; re-run with --yes to confirm deletion")
				}
				if !confirm(cmd, fmt.Sprintf("Delete profile %q? This moves its files to a backup.", name)) {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
			}

			var bkDir string
			err = withLockedState(s.Paths, func(s *state) error {
				dir, ierr := backup.New(s.Paths.BackupsDir, "pre-delete-"+name)
				if ierr != nil {
					return ierr
				}
				if _, ierr := profile.Delete(s.Paths, name, dir); ierr != nil {
					return ierr
				}
				bkDir = dir

				// Remove alias blocks from standard shellrc locations.
				for _, rc := range []string{".zshrc", ".bashrc", ".bash_profile", ".config/fish/config.fish"} {
					_ = profile.UninstallAlias(filepath.Join(s.Paths.Home, rc), name)
				}

				if s.Manifest.ActiveProfile == name {
					s.Manifest.ActiveProfile = ""
				}
				return backup.Prune(s.Paths.BackupsDir, backup.DefaultRetention)
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted profile %q. Backup: %s\n", name, s.Paths.ToHomeRelative(bkDir))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	return cmd
}

func confirm(cmd *cobra.Command, prompt string) bool {
	fmt.Fprintf(cmd.OutOrStdout(), "%s [y/N] ", prompt)
	r := bufio.NewReader(cmd.InOrStdin())
	line, err := r.ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}
