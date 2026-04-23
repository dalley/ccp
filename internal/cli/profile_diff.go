package cli

import (
	"fmt"

	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

func newProfileDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <a> [b]",
		Short: "Diff two profiles (defaults b = active profile)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := loadState()
			if err != nil {
				return err
			}
			aName := args[0]
			var bName string
			if len(args) == 2 {
				bName = args[1]
			} else {
				bName = s.Manifest.ActiveProfile
				if bName == "" {
					return fmt.Errorf("no second profile given and no active profile set")
				}
			}
			a := profile.New(s.Paths, aName)
			b := profile.New(s.Paths, bName)
			if !a.Exists() {
				return fmt.Errorf("profile %q not found", aName)
			}
			if !b.Exists() {
				return fmt.Errorf("profile %q not found", bName)
			}

			entries, err := profile.Diff(a, b)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(entries) == 0 {
				fmt.Fprintf(out, "%s and %s are identical\n", aName, bName)
				return nil
			}
			fmt.Fprintf(out, "Diff %s vs %s:\n", aName, bName)
			for _, e := range entries {
				marker := map[profile.DiffKind]string{
					profile.DiffOnlyInA:      fmt.Sprintf("- only in %s", aName),
					profile.DiffOnlyInB:      fmt.Sprintf("+ only in %s", bName),
					profile.DiffChanged:      "~ changed",
					profile.DiffTypeMismatch: "! file/dir type mismatch",
				}[e.Kind]
				fmt.Fprintf(out, "  %s  %s\n", marker, e.Path)
			}
			return nil
		},
	}
}
