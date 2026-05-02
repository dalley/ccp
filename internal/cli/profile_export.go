package cli

import (
	"fmt"
	"os"

	"github.com/dalley/ccp/internal/profile"
	"github.com/dalley/ccp/internal/refs"
	"github.com/dalley/ccp/internal/secret"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// exportStdoutIsTTY lets tests stub the TTY probe for stdout. The `-o`-unset
// path refuses when stdout is a tty (same guard git-archive applies) — tests
// need to flip this off to verify behavior without juggling pty fixtures.
var exportStdoutIsTTY = func(f *os.File) bool {
	if f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// exportStdinIsTTY mirrors exportStdoutIsTTY for the stdin confirm-path
// guard: `--include-secrets` without `--yes-really` refuses when stdin
// isn't a tty.
var exportStdinIsTTY = func(f *os.File) bool {
	if f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// newProfileExportCmd wires `ccp profile export <name> [-o path]`.
//
// Default posture: strip secrets. `{{ ref }}` tokens travel VERBATIM so
// the recipient can resolve against their own keychain. The per-profile
// secrets file is omitted entirely.
//
// `--include-secrets` resolves refs inline and ships the secrets file as
// a tarball entry — gated behind a TTY confirmation (or --yes-really in
// automation contexts). The audit scan runs ADVISORY by default (prints
// a hint to stderr but proceeds); `--fail-on-audit` turns it into a
// hard gate.
func newProfileExportCmd() *cobra.Command {
	var (
		outPath        string
		includeSecrets bool
		yesReally      bool
		failOnAudit    bool
		skipAudit      bool
	)
	cmd := &cobra.Command{
		Use:   "export <name>",
		Short: "Export a profile as a portable tar archive",
		Long: "Writes a tar of the profile's source tree to stdout or the path given " +
			"by -o. By default, `{{ ref }}` tokens travel verbatim and the per-profile " +
			"secrets file is omitted so the tarball is safe to share. " +
			"--include-secrets resolves refs inline and embeds the secrets file; that " +
			"mode requires a TTY confirmation, or --yes-really in automation. " +
			"The audit scan runs ADVISORY by default (prints a hint to stderr); " +
			"--fail-on-audit turns it into a hard gate that aborts the export on " +
			"any real finding.",
		Example: "  # portable tarball to stdout (refs stay verbatim)\n" +
			"  ccp profile export work -o work.tar\n\n" +
			"  # inline-resolve secrets (non-interactive contexts need --yes-really)\n" +
			"  ccp profile export work --include-secrets --yes-really -o work-full.tar\n\n" +
			"  # refuse export if the audit finds suspected secrets\n" +
			"  ccp profile export work --fail-on-audit -o work.tar",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProfileName,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			s, err := loadState()
			if err != nil {
				return err
			}

			// Prompt / TTY guards for --include-secrets.
			if includeSecrets {
				if !yesReally {
					// Refuse non-interactive contexts to avoid a
					// surprise "you piped secret bytes into the
					// wrong pipeline" incident. Mirrors the
					// `profile delete --yes` idiom.
					if !exportStdinIsTTY(os.Stdin) {
						return fmt.Errorf("--include-secrets requires a TTY for confirmation; re-run with --yes-really for non-interactive use")
					}
					if !confirm(cmd, fmt.Sprintf("this tarball will contain resolved secrets for %q; continue?", name)) {
						// ExitUser (1): the user actively
						// declined, not a state problem.
						return fmt.Errorf("aborted by user")
					}
				}
			}

			// Output target. `-o` unset → stdout, but only if stdout
			// isn't a TTY (tar bytes in a terminal are useless and
			// annoying). `-o` set → file, 0600.
			var writer *os.File
			var closeWhenDone func() error
			if outPath == "" {
				if exportStdoutIsTTY(osStdoutForExport(cmd)) {
					return fmt.Errorf("refusing to write a tar archive to a terminal; re-run with -o <path> or redirect stdout to a file/pipe")
				}
				// cmd.OutOrStdout() for test capture. Tar bytes
				// travel through cobra's writer unchanged.
				if err := streamExport(cmd, s, name, includeSecrets, failOnAudit, skipAudit); err != nil {
					return err
				}
				return nil
			}

			f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
			if err != nil {
				return fmt.Errorf("create %s: %w", outPath, err)
			}
			writer = f
			closeWhenDone = f.Close

			// Fix perm defensively in case umask stripped group/world
			// mode bits the user expected — the 0o600 at open time
			// should already be tight but an umask of 0 would have
			// given 0o600 regardless; this chmod is belt-and-suspenders.
			if err := os.Chmod(outPath, 0o600); err != nil {
				_ = closeWhenDone()
				return fmt.Errorf("chmod %s: %w", outPath, err)
			}

			if err := streamExportTo(cmd, writer, s, name, includeSecrets, failOnAudit, skipAudit); err != nil {
				_ = closeWhenDone()
				return err
			}
			return closeWhenDone()
		},
	}
	cmd.Flags().StringVarP(&outPath, "output", "o", "", "write tar to this path (0600); default stdout")
	cmd.Flags().BoolVar(&includeSecrets, "include-secrets", false, "resolve refs inline and include the per-profile secrets file")
	cmd.Flags().BoolVar(&yesReally, "yes-really", false, "bypass the TTY confirmation for --include-secrets (required in non-TTY contexts)")
	cmd.Flags().BoolVar(&failOnAudit, "fail-on-audit", false, "refuse to export if profile.Audit finds suspected secrets")
	cmd.Flags().BoolVar(&skipAudit, "skip-audit", false, "don't run the audit scan at all")
	return cmd
}

// streamExport writes the tar stream to cmd.OutOrStdout(). Split from the
// file-destination path so the stdout case can use cobra's writer (tests
// capture it) without an intermediate os.File.
func streamExport(cmd *cobra.Command, s state, name string, includeSecrets, failOnAudit, skipAudit bool) error {
	return streamExportTo(cmd, nil, s, name, includeSecrets, failOnAudit, skipAudit)
}

// streamExportTo runs profile.Export against the chosen writer. dest==nil
// means "use cmd.OutOrStdout()"; otherwise dest is the file. The split
// exists because we need to close the file on the success path, but
// MUST NOT close cmd's writer (cobra owns it).
func streamExportTo(cmd *cobra.Command, dest *os.File, s state, name string, includeSecrets, failOnAudit, skipAudit bool) error {
	// Audit findings leak through this pointer so we can print the
	// advisory hint without rescanning. The slice stays empty when
	// SkipAudit is set.
	var findings []profile.AuditFinding
	opts := profile.ExportOptions{
		IncludeSecrets: includeSecrets,
		FailOnAudit:    failOnAudit,
		SkipAudit:      skipAudit,
		AuditAdvisory:  &findings,
	}
	if includeSecrets {
		// Wire secret.Get through the DefaultResolver. Matches the
		// public-API contract ("resolve each ref via refs.Render
		// using refs.DefaultResolver{Profile: name, KeyringGet:
		// secret.Get, EnvLookup: os.LookupEnv}").
		opts.Resolver = refs.DefaultResolver{
			Profile: name,
			KeyringGet: func(_, _, key string) (string, error) {
				return secret.Get(s.Paths, name, key)
			},
			EnvLookup: os.LookupEnv,
		}
	}

	w := cmd.OutOrStdout()
	if dest != nil {
		w = dest
	}
	if err := profile.Export(s.Paths, name, opts, w); err != nil {
		return err
	}

	// Advisory banner: always stderr (stdout may be carrying tar bytes).
	// Only print when findings ran through (not SkipAudit, not
	// FailOnAudit-that-already-errored) and there ARE real findings.
	if !skipAudit && !failOnAudit {
		realCount := 0
		for _, f := range findings {
			switch f.Kind {
			case "skipped-large", "skipped-binary":
				// informational — don't surface in advisory hint
			default:
				realCount++
			}
		}
		if realCount > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"ccp: %d suspected secret(s) detected in profile %q; review with `ccp profile audit %s`\n",
				realCount, name, name)
		}
	}
	return nil
}

// osStdoutForExport returns the *os.File that cmd.OutOrStdout() ultimately
// wraps when it's stdout (vs. a test buffer). Tests inject a bytes.Buffer
// via SetOut, so this helper returns nil in tests — which propagates
// through exportStdoutIsTTY as "not a TTY" and lets the test path run.
func osStdoutForExport(cmd *cobra.Command) *os.File {
	if cmd.OutOrStdout() == os.Stdout {
		return os.Stdout
	}
	return nil
}
