package cli

import (
	"encoding/json"
	"fmt"

	"github.com/dalley/ccp/internal/profile"
	"github.com/spf13/cobra"
)

// newProfileAuditCmd wires `ccp profile audit <name>`. Pattern follows
// profile_doctor.go: human-readable summary on stdout when run without
// --json, JSON array on stdout with --json, and a typed sentinel return
// when findings exist so ExitCodeFor maps us to ExitConflict (4).
func newProfileAuditCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:               "audit <name>",
		Short:             "Scan a profile for suspected secrets",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProfileName,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := loadState()
			if err != nil {
				return err
			}
			name := args[0]
			findings, err := profile.Audit(s.Paths, name)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()

			if asJSON {
				// Always emit a structured payload, even on the
				// nonzero-exit path: agents shouldn't have to
				// parse stderr to distinguish "clean" from "found
				// secrets".
				if findings == nil {
					findings = []profile.AuditFinding{}
				}
				payload := struct {
					Profile  string                 `json:"profile"`
					Detected int                    `json:"detected"`
					Findings []profile.AuditFinding `json:"findings"`
				}{
					Profile:  name,
					Detected: profile.CountReal(findings),
					Findings: findings,
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(payload); err != nil {
					return err
				}
			} else if len(findings) == 0 {
				// Keep stdout silent for scriptability; humans
				// see the "clean" message on stderr.
				fmt.Fprintf(errOut, "no suspected secrets detected in profile %s\n", name)
			} else {
				for _, f := range findings {
					fmt.Fprintf(out, "%s:%d [%s] %s\n", f.File, f.Line, f.Kind, f.Preview)
				}
			}

			// Differentiate "real findings" from purely informational
			// skipped-* entries: a file we couldn't scan shouldn't
			// trigger the conflict exit — that would cause agents
			// to treat a binary blob as a security incident.
			if profile.CountReal(findings) > 0 {
				return profile.ErrAuditSecretsDetected
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON {profile, detected, findings}")
	return cmd
}
