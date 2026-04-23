package cli

import (
	"encoding/json"
	"fmt"
	"time"

	ccpsync "github.com/dalley/ccp/internal/sync"
	"github.com/spf13/cobra"
)

func newSyncStatusCmd() *cobra.Command {
	var (
		asJSON  bool
		fetch   bool
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show sync status (remote, dirty files, current branch)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := loadState()
			if err != nil {
				return err
			}
			var ss ccpsync.StatusSummary
			if fetch {
				ss, err = ccpsync.StatusWithFetch(s.Paths.ConfigDir, timeout)
			} else {
				ss, err = ccpsync.Status(s.Paths.ConfigDir)
			}
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(ss)
			}
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
			if ss.Fetched {
				switch {
				case ss.Ahead == 0 && ss.Behind == 0:
					fmt.Fprintf(out, "Remote:  up to date with origin/%s\n", ss.CurrentBranch)
				default:
					fmt.Fprintf(out, "Remote:  %d ahead, %d behind origin/%s\n", ss.Ahead, ss.Behind, ss.CurrentBranch)
				}
			} else if fetch && ss.FetchError != "" {
				fmt.Fprintf(out, "Fetch:   failed (%s)\n", ss.FetchError)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit StatusSummary as JSON")
	cmd.Flags().BoolVar(&fetch, "fetch", false, "fetch origin to compute ahead/behind against origin/<branch>")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "deadline for the fetch (only applies with --fetch)")
	return cmd
}

func fallback(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}
