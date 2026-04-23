package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

func newProfileShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show [name]",
		Short: "Show details of a profile",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := loadState()
			if err != nil {
				return err
			}
			name := s.Manifest.ActiveProfile
			if len(args) == 1 {
				name = args[0]
			}
			if name == "" {
				return fmt.Errorf("no profile specified and no active profile set")
			}
			pr := profile.New(s.Paths, name)
			if !pr.Exists() {
				return fmt.Errorf("profile %q not found", name)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Profile:     %s\n", pr.Name)
			if name == s.Manifest.ActiveProfile {
				fmt.Fprintln(out, "Active:      yes")
			}
			fmt.Fprintf(out, "Source dir:  %s\n", s.Paths.ToHomeRelative(pr.SourceDir))
			fmt.Fprintf(out, "Runtime dir: %s\n", s.Paths.ToHomeRelative(pr.ConfigDir))
			fmt.Fprintln(out, "Items:")
			for _, item := range profile.SharedItems {
				status := "absent"
				full := filepath.Join(pr.SourceDir, item.Name)
				if info, err := os.Stat(full); err == nil {
					status = "present"
					if item.Dir && info.IsDir() {
						if n, err := countEntries(full); err == nil {
							status = fmt.Sprintf("present (%d entries)", n)
						}
					}
				}
				fmt.Fprintf(out, "  %-18s %s\n", item.Name, status)
			}
			return nil
		},
	}
}

func countEntries(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}
