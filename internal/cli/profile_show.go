package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

// showReport is the JSON shape of `ccp profile show --json`.
type showReport struct {
	Name       string            `json:"name"`
	Active     bool              `json:"active"`
	SourceDir  string            `json:"source_dir"`
	ConfigDir  string            `json:"config_dir"`
	Items      map[string]string `json:"items"`
	ItemCounts map[string]int    `json:"item_counts,omitempty"`
}

func newProfileShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:               "show [name]",
		Short:             "Show details of a profile",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeProfileName,
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
			pr, err := profile.NewChecked(s.Paths, name)
			if err != nil {
				return err
			}
			if !pr.Exists() {
				return fmt.Errorf("%w: %s", profile.ErrNotFound, name)
			}

			rep := showReport{
				Name:       pr.Name,
				Active:     name == s.Manifest.ActiveProfile,
				SourceDir:  s.Paths.ToHomeRelative(pr.SourceDir),
				ConfigDir:  s.Paths.ToHomeRelative(pr.ConfigDir),
				Items:      map[string]string{},
				ItemCounts: map[string]int{},
			}
			for _, item := range profile.SharedItems {
				full := filepath.Join(pr.SourceDir, item.Name)
				info, err := os.Stat(full)
				if err != nil {
					rep.Items[item.Name] = "absent"
					continue
				}
				rep.Items[item.Name] = "present"
				if item.Dir && info.IsDir() {
					if n, err := countEntries(full); err == nil {
						rep.ItemCounts[item.Name] = n
					}
				}
			}

			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Profile:     %s\n", rep.Name)
			if rep.Active {
				fmt.Fprintln(out, "Active:      yes")
			}
			fmt.Fprintf(out, "Source dir:  %s\n", rep.SourceDir)
			fmt.Fprintf(out, "Runtime dir: %s\n", rep.ConfigDir)
			fmt.Fprintln(out, "Items:")
			for _, item := range profile.SharedItems {
				status := rep.Items[item.Name]
				if n, ok := rep.ItemCounts[item.Name]; ok {
					status = fmt.Sprintf("%s (%d entries)", status, n)
				}
				fmt.Fprintf(out, "  %-18s %s\n", item.Name, status)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func countEntries(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}
