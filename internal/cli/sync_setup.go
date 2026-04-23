package cli

import (
	"errors"
	"fmt"

	ccpsync "github.com/dalley/ccp/internal/sync"
	"github.com/spf13/cobra"
)

func newSyncSetupCmd() *cobra.Command {
	var url string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Initialize ~/.config/ccp as a Git repo and (optionally) link a remote",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := loadState()
			if err != nil {
				return err
			}

			return withLock(s.Paths, func() error {
				// If a URL is given AND the local dir has no repo yet, try
				// to clone into place — copies existing tracked content
				// onto this machine.
				existing, err := ccpsync.IsSyncRepo(s.Paths.ConfigDir)
				if err != nil {
					return err
				}
				if url != "" && !existing {
					err := ccpsync.CloneOrOpen(s.Paths.ConfigDir, url)
					// Empty remote is expected when this is the FIRST
					// machine to set up against a freshly-created remote:
					// fall through to InitRepo + SetRemote below.
					if err != nil && !errors.Is(err, ccpsync.ErrRemoteEmpty) {
						return err
					}
				}
				// Initialize (or idempotently re-initialize) the repo,
				// writing .gitignore + marker and making an initial commit
				// if needed.
				if err := ccpsync.InitRepo(s.Paths.ConfigDir); err != nil {
					return err
				}
				if url != "" {
					if err := ccpsync.SetRemote(s.Paths.ConfigDir, url); err != nil {
						return fmt.Errorf("set remote: %w", err)
					}
				}

				// Verify the repo is ccp-managed (if it came from a clone,
				// this guards against bonding with an arbitrary repo).
				marker, err := ccpsync.ReadMarker(s.Paths.ConfigDir)
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "Sync ready at %s\n", s.Paths.ToHomeRelative(s.Paths.ConfigDir))
				if url != "" {
					fmt.Fprintf(out, "Remote:  %s\n", url)
				}
				if marker != nil {
					fmt.Fprintf(out, "Marker:  managedBy=%s version=%d\n", marker.ManagedBy, marker.Version)
				}
				fmt.Fprintln(out, "Next: ccp sync push  (commit + push to origin)")
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "git remote URL (origin)")
	return cmd
}
