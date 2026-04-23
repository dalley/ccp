package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

func newProfileCreateCmd() *cobra.Command {
	var (
		fromCurrent bool
		fromProfile string
		withAlias   bool
		shellrc     string
		asJSON      bool
	)
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			s, err := loadState()
			if err != nil {
				return err
			}

			var pr profile.Profile
			err = withLock(s.Paths, func() error {
				var ierr error
				pr, ierr = profile.Create(s.Paths, name, profile.CreateOptions{
					FromCurrent: fromCurrent,
					FromProfile: fromProfile,
				})
				if ierr != nil {
					return ierr
				}
				if withAlias {
					rc, rerr := resolveShellrc(s.Paths.Home, shellrc)
					if rerr != nil {
						return rerr
					}
					return profile.InstallAlias(rc, name)
				}
				return nil
			})
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if asJSON {
				rep := struct {
					Name           string `json:"name"`
					SourceDir      string `json:"source_dir"`
					ConfigDir      string `json:"config_dir"`
					AliasInstalled bool   `json:"alias_installed"`
				}{
					Name:           pr.Name,
					SourceDir:      s.Paths.ToHomeRelative(pr.SourceDir),
					ConfigDir:      s.Paths.ToHomeRelative(pr.ConfigDir),
					AliasInstalled: withAlias,
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			}
			fmt.Fprintf(out, "Created profile %q\n", pr.Name)
			fmt.Fprintf(out, "  Source: %s\n", s.Paths.ToHomeRelative(pr.SourceDir))
			fmt.Fprintf(out, "  Runtime: %s\n", s.Paths.ToHomeRelative(pr.ConfigDir))
			if withAlias {
				fmt.Fprintf(out, "Alias `claude-%s` installed; reload your shell to use it.\n", name)
			} else {
				fmt.Fprintf(out, "Activate with: ccp use %s\n", name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromCurrent, "from-current", false, "seed from ~/.claude/")
	cmd.Flags().StringVar(&fromProfile, "from", "", "seed from an existing profile")
	cmd.Flags().BoolVar(&withAlias, "alias", false, "install a shell alias `claude-<name>` that launches Claude with this profile")
	cmd.Flags().StringVar(&shellrc, "shellrc", "", "path to the shellrc to write the alias into (defaults to ~/.zshrc or ~/.bashrc)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON report instead of prose")
	// Dynamic completion for --from: offer real profile names instead of
	// falling back to filesystem path completion.
	_ = cmd.RegisterFlagCompletionFunc("from", completeProfileName)
	return cmd
}

// resolveShellrc picks a shellrc file to operate on. Explicit path wins.
// Otherwise we default to ~/.zshrc (most common on macOS), falling back to
// ~/.bashrc if that's what exists, falling back to creating ~/.zshrc.
func resolveShellrc(home, explicit string) (string, error) {
	if explicit != "" {
		// Only expand the canonical tilde form "~/..." — treat a bare "~"
		// or "~name" as literal so a surprising expansion never writes to
		// the home directory or to a sibling of it.
		if explicit == "~" {
			return "", fmt.Errorf("--shellrc cannot be just \"~\" (would target the home directory itself)")
		}
		if strings.HasPrefix(explicit, "~/") {
			explicit = filepath.Join(home, explicit[2:])
		}
		return explicit, nil
	}
	for _, candidate := range []string{".zshrc", ".bashrc", ".bash_profile"} {
		full := filepath.Join(home, candidate)
		if _, err := os.Stat(full); err == nil {
			return full, nil
		}
	}
	return filepath.Join(home, ".zshrc"), nil
}
