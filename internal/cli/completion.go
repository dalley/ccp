package cli

import (
	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

// completeProfileName returns the list of valid profile names for dynamic
// shell completion. Returns cobra.ShellCompDirectiveNoFileComp so completion
// doesn't fall back to file names when the profiles list is empty.
//
// Wire this on every command that accepts a profile-name positional:
//
//	cmd.ValidArgsFunction = completeProfileName
//
// For commands that take TWO names (diff <a> [b]) this is still correct for
// both positions — we offer the same profile set regardless of position.
func completeProfileName(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	s, err := loadState()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	prs, err := profile.List(s.Paths)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	names := make([]string, 0, len(prs))
	for _, p := range prs {
		names = append(names, p.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
