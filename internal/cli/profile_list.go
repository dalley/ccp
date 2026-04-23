package cli

import (
	"encoding/json"
	"fmt"

	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

func newProfileListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List profiles",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := loadState()
			if err != nil {
				return err
			}
			profiles, err := profile.List(s.Paths)
			if err != nil {
				return err
			}
			active := s.Manifest.ActiveProfile

			if asJSON {
				return emitListJSON(cmd, profiles, active)
			}
			if len(profiles) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No profiles yet. Create one with: ccp profile create <name>")
				return nil
			}
			for _, pr := range profiles {
				marker := "  "
				if pr.Name == active {
					marker = "* "
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s%s\n", marker, pr.Name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func newCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Print the active profile name",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := loadState()
			if err != nil {
				return err
			}
			if s.Manifest.ActiveProfile == "" {
				return nil
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), s.Manifest.ActiveProfile)
			return err
		},
	}
}

func emitListJSON(cmd *cobra.Command, profiles []profile.Profile, active string) error {
	type row struct {
		Name   string `json:"name"`
		Active bool   `json:"active"`
	}
	out := make([]row, 0, len(profiles))
	for _, pr := range profiles {
		out = append(out, row{Name: pr.Name, Active: pr.Name == active})
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
