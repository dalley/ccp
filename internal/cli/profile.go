package cli

import "github.com/spf13/cobra"

func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage Claude Code profiles",
	}
	cmd.AddCommand(newProfileListCmd())
	cmd.AddCommand(newProfileShowCmd())
	cmd.AddCommand(newProfileCreateCmd())
	cmd.AddCommand(newProfileUseCmd())
	cmd.AddCommand(newProfileDeleteCmd())
	cmd.AddCommand(newProfileRenameCmd())
	cmd.AddCommand(newProfileDiffCmd())
	cmd.AddCommand(newProfileDoctorCmd())
	cmd.AddCommand(newProfileAuditCmd())
	cmd.AddCommand(newProfileRollbackCmd())
	cmd.AddCommand(newProfileRefreshCmd())
	return cmd
}
