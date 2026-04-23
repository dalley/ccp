package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags "-X github.com/dalley/ccp/internal/cli.Version=..."
var Version = "0.1.0-dev"

func newVersionCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print ccp version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]string{"version": Version})
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), Version)
			return err
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit {\"version\": <semver>}")
	return cmd
}
