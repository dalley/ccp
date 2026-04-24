package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

func newProfileRenameCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:               "rename <old> <new>",
		Short:             "Rename a profile",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeProfileName,
		RunE: func(cmd *cobra.Command, args []string) error {
			oldName, newName := args[0], args[1]
			s, err := loadState()
			if err != nil {
				return err
			}
			// Cap the post-rename BuildSymlinks so a hung `op read` in a
			// profile that happens to use `{{ op://… }}` refs can't pin
			// `ccp profile rename` on the user's terminal. Same budget
			// `ccp exec` uses for its pre-child refresh.
			ctx, cancel := context.WithTimeout(cmd.Context(), ExecRefreshTimeout)
			defer cancel()

			hadAlias := false
			err = withLockedState(s.Paths, func(s *state) error {
				if ierr := profile.RenameCtx(ctx, s.Paths, oldName, newName); ierr != nil {
					return ierr
				}
				// Remove any existing alias block for the old name. Detect
				// whether one existed so we can warn the user — we don't
				// automatically reinstall under the new name because the
				// user's shellrc may not be the right target.
				for _, rc := range []string{".zshrc", ".bashrc", ".bash_profile", ".config/fish/config.fish"} {
					path := filepath.Join(s.Paths.Home, rc)
					if profile.AliasExists(path, oldName) {
						hadAlias = true
					}
					_ = profile.UninstallAlias(path, oldName)
				}
				if s.Manifest.ActiveProfile == oldName {
					s.Manifest.ActiveProfile = newName
				}
				return nil
			})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				rep := struct {
					Old          string `json:"old"`
					New          string `json:"new"`
					AliasRemoved bool   `json:"alias_removed"`
				}{oldName, newName, hadAlias}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			}
			fmt.Fprintf(out, "Renamed %q → %q\n", oldName, newName)
			if hadAlias {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"note: the `claude-%s` shell alias was removed. Re-add it for %q with:\n"+
						"  ccp profile create %s --alias --from %s    # or hand-edit your shellrc\n",
					oldName, newName, newName, newName)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON report instead of prose")
	return cmd
}
