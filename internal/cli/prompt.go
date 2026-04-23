package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newPromptCmd() *cobra.Command {
	var (
		prefix string
		suffix string
	)
	cmd := &cobra.Command{
		Use:   "prompt",
		Short: "Print the active profile for embedding in a shell prompt",
		Long: `Prints the active profile name, ready to drop into your prompt. Prints
nothing when no profile is active, so users can concatenate unconditionally.

Examples:
  PS1='$(ccp prompt --prefix "[" --suffix "] ")%~ $ '
  starship:  format = "$(ccp prompt --prefix '[ccp:' --suffix ']')\n$character"
`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := loadState()
			if err != nil {
				// Prompt must not error out — silently print nothing if
				// something is wrong. A broken prompt is hostile.
				return nil
			}
			name := s.Manifest.ActiveProfile
			if name == "" {
				return nil
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s%s%s", prefix, name, suffix)
			return err
		},
	}
	cmd.Flags().StringVar(&prefix, "prefix", "", "text to print before the profile name")
	cmd.Flags().StringVar(&suffix, "suffix", "", "text to print after the profile name")
	return cmd
}
