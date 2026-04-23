package cli

import (
	"fmt"

	"github.com/dalley/ccp/internal/fslock"
	"github.com/dalley/ccp/internal/manifest"
	"github.com/dalley/ccp/internal/paths"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	var shell string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create ccp's config directory and manifest",
		Long: `Create ~/.config/ccp/ and a default manifest.toml. Safe to re-run;
existing state is preserved. Prints the one-line shell snippet you should
add to your shellrc so that the active profile takes effect automatically.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := paths.Resolve()
			if err != nil {
				return err
			}
			if err := p.Ensure(); err != nil {
				return err
			}

			lock, err := fslock.Acquire(p.LockPath)
			if err != nil {
				return err
			}
			defer func() { _ = lock.Release() }()

			m, existed, err := manifest.Load(p.ManifestPath)
			if err != nil {
				return err
			}
			if shell != "" {
				m.DefaultShell = shell
			}
			if err := manifest.Save(p.ManifestPath, m); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if existed {
				fmt.Fprintf(out, "ccp already initialized at %s\n", p.ToHomeRelative(p.ConfigDir))
			} else {
				fmt.Fprintf(out, "Initialized ccp at %s\n", p.ToHomeRelative(p.ConfigDir))
			}

			sh := m.DefaultShell
			if sh == "" {
				sh = "zsh"
			}
			fmt.Fprintln(out)
			fmt.Fprintln(out, "Add this line to your shellrc so ccp activates the selected profile:")
			fmt.Fprintf(out, "  eval \"$(ccp shell-init %s)\"\n", sh)
			return nil
		},
	}
	cmd.Flags().StringVar(&shell, "shell", "", "record default shell (zsh, bash, fish) in manifest")
	return cmd
}
