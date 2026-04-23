package cli

import (
	"fmt"

	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

func newProfileDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor [name]",
		Short: "Validate profile integrity (all profiles, or just one)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := loadState()
			if err != nil {
				return err
			}
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			findings, err := profile.Doctor(s.Paths, name)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(findings) == 0 {
				fmt.Fprintln(out, "All profiles healthy.")
				return nil
			}
			errorsFound := 0
			for _, f := range findings {
				prefix := "[warn]"
				if f.Severity == "error" {
					prefix = "[error]"
					errorsFound++
				}
				scope := f.Profile
				if scope == "" {
					scope = "(global)"
				}
				fmt.Fprintf(out, "%s %s: %s\n", prefix, scope, f.Message)
				if f.Hint != "" {
					fmt.Fprintf(out, "        hint: %s\n", f.Hint)
				}
			}
			if errorsFound > 0 {
				return fmt.Errorf("%d error(s) found", errorsFound)
			}
			return nil
		},
	}
}
