package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	ccpsync "github.com/dalley/ccp/internal/sync"
	"github.com/spf13/cobra"
)

func newSyncSetupCmd() *cobra.Command {
	var (
		url    string
		force  bool
		asJSON bool
	)
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

				// Verify a .ccp-sync.json marker with managedBy=ccp is
				// present whenever we're adopting an existing on-disk .git
				// — even if it was left behind by a half-completed clone
				// on a previous invocation. Without this check, a user
				// running `ccp sync setup --url <hostile>` where a partial
				// clone succeeded earlier would adopt the hostile remote
				// because IsSyncRepo=true skips the clone-path check.
				//
				// The check is skipped when there's no .git yet (fresh
				// first-ever setup against an empty remote, pre-InitRepo)
				// and when --force is set.
				repoPresent, _ := ccpsync.IsSyncRepo(s.Paths.ConfigDir)
				if repoPresent && !force {
					marker, err := ccpsync.ReadMarker(s.Paths.ConfigDir)
					if err != nil {
						return err
					}
					if marker == nil || marker.ManagedBy != "ccp" {
						_ = os.RemoveAll(s.Paths.ConfigDir + "/.git")
						_ = os.RemoveAll(s.Paths.ProfilesDir)
						remoteDesc := url
						if remoteDesc == "" {
							remoteDesc = s.Paths.ConfigDir
						}
						return fmt.Errorf("refusing to adopt %s: missing or invalid .ccp-sync.json marker (expected managedBy=\"ccp\"). "+
							"Re-run with --force to adopt anyway, or point --url at a ccp sync repo.", remoteDesc)
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

				marker, err := ccpsync.ReadMarker(s.Paths.ConfigDir)
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				if asJSON {
					rep := struct {
						ConfigDir string          `json:"config_dir"`
						Remote    string          `json:"remote,omitempty"`
						Marker    *ccpsync.Marker `json:"marker,omitempty"`
					}{
						ConfigDir: s.Paths.ToHomeRelative(s.Paths.ConfigDir),
						Remote:    url,
						Marker:    marker,
					}
					enc := json.NewEncoder(out)
					enc.SetIndent("", "  ")
					return enc.Encode(rep)
				}
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
	cmd.Flags().BoolVar(&force, "force", false, "adopt a remote even when it lacks a valid ccp-sync marker")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON report instead of prose")
	return cmd
}
