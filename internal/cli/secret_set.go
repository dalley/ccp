//go:build !windows

package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dalley/ccp/internal/profile"
	"github.com/dalley/ccp/internal/secret"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// newSecretSetCmd wires `ccp secret set <profile> <key> [value]`.
//
// Value-source precedence (first match wins):
//
//  1. Third positional arg. Most convenient on an interactive shell; also
//     the easiest form for scripts that don't mind the value appearing in
//     process listings.
//  2. `--value <v>` flag. Same ergonomics as the positional form but more
//     explicit; useful when wrapping the command in aliases.
//  3. `--stdin`. Read all of stdin as the value. Trims a single trailing
//     '\n' so `echo "$SECRET" | ccp secret set ...` does the right thing
//     without the caller having to strip newlines.
//  4. Interactive TTY prompt. If stdin is a terminal and none of the above
//     were supplied, prompt for the value with echo disabled (golang.org/x
//     /term.ReadPassword). A confirmation prompt is NOT required — this
//     command is idempotent and overwrite is the whole point of `set`.
//
// Non-TTY with no value source refuses with an error naming the three
// explicit ways to pass a value. Matches the TTY-refusal discipline in
// internal/cli/profile_delete.go: an agent piping /dev/null should get a
// loud, actionable error, not a silent empty-value write.
//
// Goes through withLockedState so concurrent `secret set` calls and other
// ccp state mutations (profile create, manifest writes) don't interleave —
// the internal/secret package itself is lock-free by design and expects
// the CLI to serialize writers.
func newSecretSetCmd() *cobra.Command {
	var (
		valueFlag string
		readStdin bool
	)
	cmd := &cobra.Command{
		Use:   "set <profile> <key> [value]",
		Short: "Store a secret value for <profile>/<key>",
		Long: "Stores a value under <profile>/<key> in the OS keychain (file-fallback " +
			"on headless systems). Value source precedence: positional > --value > " +
			"--stdin > interactive TTY prompt. Non-TTY contexts with no source refuse " +
			"loudly. --stdin strips exactly one trailing newline so `echo $SECRET | " +
			"ccp secret set ...` stores $SECRET unchanged.",
		Example: "  # interactive TTY prompt (echo disabled)\n" +
			"  ccp secret set work API_KEY\n\n" +
			"  # explicit flag (exposed in process listing)\n" +
			"  ccp secret set work API_KEY --value s3cret\n\n" +
			"  # pipe from a file or another command\n" +
			"  pass show work/api | ccp secret set work API_KEY --stdin",
		Args:              cobra.RangeArgs(2, 3),
		ValidArgsFunction: completeProfileName,
		RunE: func(cmd *cobra.Command, args []string) error {
			prof := args[0]
			key := args[1]

			if err := profile.ValidateName(prof); err != nil {
				return err
			}
			if err := secret.ValidateKey(key); err != nil {
				return err
			}

			value, err := resolveSecretValue(cmd, args, valueFlag, readStdin)
			if err != nil {
				return err
			}

			s, err := loadState()
			if err != nil {
				return err
			}
			if err := withLockedState(s.Paths, func(s *state) error {
				return secret.Set(s.Paths, prof, key, value)
			}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Stored secret %s/%s\n", prof, key)
			return nil
		},
	}
	cmd.Flags().StringVar(&valueFlag, "value", "", "secret value (alternative to positional arg)")
	cmd.Flags().BoolVar(&readStdin, "stdin", false, "read secret value from stdin (trailing newline stripped)")
	return cmd
}

// resolveSecretValue implements the value-source precedence documented on
// newSecretSetCmd. Extracted so the TTY-refuse path is testable in isolation
// and the RunE body stays short.
func resolveSecretValue(cmd *cobra.Command, args []string, valueFlag string, readStdin bool) (string, error) {
	// 1. Positional wins.
	if len(args) == 3 {
		return args[2], nil
	}
	// 2. --value flag.
	//
	// Flag presence matters, not just non-empty — a caller explicitly
	// passing --value="" deserves to succeed (empty string IS a valid
	// secret value). cobra's Lookup().Changed reports whether the flag
	// was set on the command line vs left at its zero default.
	if f := cmd.Flags().Lookup("value"); f != nil && f.Changed {
		return valueFlag, nil
	}
	// 3. --stdin.
	if readStdin {
		b, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		s := string(b)
		// Strip exactly one trailing newline so `echo $X | ccp secret set`
		// stores `$X` not `$X\n`. Don't TrimRight — a value that legitimately
		// ends in "\n\n" should keep one of them.
		s = strings.TrimSuffix(s, "\n")
		return s, nil
	}
	// 4. Interactive TTY prompt.
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(cmd.ErrOrStderr(), "Value for %s/%s: ", args[0], args[1])
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr()) // newline after the echo-off prompt
		if err != nil {
			return "", fmt.Errorf("read value from terminal: %w", err)
		}
		return string(b), nil
	}
	// 5. Non-TTY and no source: refuse with a hint.
	return "", fmt.Errorf("no secret value supplied: pass a positional value, " +
		"use --value, or pipe stdin with --stdin")
}
