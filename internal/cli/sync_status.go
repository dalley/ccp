package cli

import (
	"fmt"

	ccpsync "github.com/dalley/ccp/internal/sync"
	"github.com/spf13/cobra"
)

func newSyncStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show sync status (remote, dirty files, current branch)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := loadState()
			if err != nil {
				return err
			}
			ss, err := ccpsync.Status(s.Paths.ConfigDir)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !ss.RepoExists {
				fmt.Fprintln(out, "Sync is not set up. Run: ccp sync setup [--url <git>]")
				return nil
			}
			fmt.Fprintf(out, "Repo:    %s\n", s.Paths.ToHomeRelative(s.Paths.ConfigDir))
			fmt.Fprintf(out, "Branch:  %s\n", fallback(ss.CurrentBranch, "(none yet)"))
			fmt.Fprintf(out, "Remote:  %s\n", fallback(ss.Remote, "(not configured)"))
			if ss.Dirty {
				fmt.Fprintf(out, "Status:  %d uncommitted file(s)\n", len(ss.ChangedFiles))
				for _, f := range ss.ChangedFiles {
					fmt.Fprintf(out, "  %s\n", f)
				}
			} else {
				fmt.Fprintln(out, "Status:  clean")
			}
			return nil
		},
	}
}

func fallback(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}
