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
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "current",
		Short: "Print the active profile name (empty when no profile is active)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := loadState()
			if err != nil {
				return err
			}
			active := s.Manifest.ActiveProfile
			if asJSON {
				type row struct {
					Active *string `json:"active"`
				}
				var v row
				if active != "" {
					v.Active = &active
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(v)
			}
			if active == "" {
				return nil
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), active)
			return err
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit {\"active\": null} or {\"active\": <name>}")
	return cmd
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
