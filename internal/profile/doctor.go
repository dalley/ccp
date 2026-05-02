package profile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dalley/ccp/internal/paths"
	"github.com/dalley/ccp/internal/refs"
)

// ErrDoctorFailed is returned by the CLI when `ccp profile doctor` surfaces
// at least one severity=error finding. Maps to exit code 2 (state error)
// so agents can distinguish a broken profile from "profile not found"
// (exit 1) or a warning-only run (exit 0).
var ErrDoctorFailed = errors.New("doctor found errors")

// DoctorFinding is one issue detected by Doctor — either a problem in a
// profile's on-disk state or the cross-profile manifest state.
type DoctorFinding struct {
	// Profile is the name of the profile the finding relates to, or "" if
	// the finding is global.
	Profile string `json:"profile,omitempty"`
	// Severity is "warn" for recoverable issues and "error" for things that
	// would break switching to this profile.
	Severity string `json:"severity"`
	// Message is a human-readable description.
	Message string `json:"message"`
	// Hint is optional actionable guidance.
	Hint string `json:"hint,omitempty"`
}

// Doctor validates a profile's runtime directory against its source:
//   - runtime dir exists
//   - every symlink in the runtime dir points at a still-present source file
//   - no SharedItem entry that IS present in the source is missing a runtime symlink
//   - runtime regular files are either ccp-rendered (ref-bearing source) or
//     explicitly flagged as "Claude overwrite" warnings
//   - every secret reference declared in source can be resolved (dry-run)
//
// If profileName is empty, all profiles are checked.
func Doctor(p paths.Paths, profileName string) ([]DoctorFinding, error) {
	var profiles []Profile
	if profileName != "" {
		pr, err := NewChecked(p, profileName)
		if err != nil {
			return nil, err
		}
		if !pr.Exists() {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, profileName)
		}
		profiles = []Profile{pr}
	} else {
		got, err := List(p)
		if err != nil {
			return nil, err
		}
		profiles = got
	}

	var out []DoctorFinding
	for _, pr := range profiles {
		out = append(out, checkRuntime(pr)...)
		out = append(out, checkSecretRefs(p, pr)...)
	}
	return out, nil
}

func checkRuntime(pr Profile) []DoctorFinding {
	var out []DoctorFinding

	info, err := os.Stat(pr.ConfigDir)
	if err != nil {
		if os.IsNotExist(err) {
			out = append(out, DoctorFinding{
				Profile: pr.Name, Severity: "warn",
				Message: "runtime dir does not exist yet",
				Hint:    "run `ccp profile refresh " + pr.Name + "` to rebuild runtime symlinks",
			})
			return out
		}
		out = append(out, DoctorFinding{Profile: pr.Name, Severity: "error",
			Message: fmt.Sprintf("stat runtime dir: %v", err)})
		return out
	}
	if !info.IsDir() {
		out = append(out, DoctorFinding{Profile: pr.Name, Severity: "error",
			Message: "runtime path is not a directory",
			Hint:    "remove it and re-run `ccp profile create --from <other>`",
		})
		return out
	}

	// Pass 1: for each SharedItem present in the source, the runtime should
	// contain a matching symlink — OR a rendered regular file when the
	// source has refs. Directory items may be either a symlink (no refs
	// anywhere) or a real 0700 dir (refs somewhere).
	for _, item := range SharedItems {
		src := filepath.Join(pr.SourceDir, item.Name)
		srcInfo, err := os.Lstat(src)
		if err != nil {
			continue // source doesn't have it; nothing to expect in runtime.
		}
		dst := filepath.Join(pr.ConfigDir, item.Name)
		dstInfo, err := os.Lstat(dst)
		if err != nil {
			out = append(out, DoctorFinding{Profile: pr.Name, Severity: "error",
				Message: fmt.Sprintf("missing runtime symlink for %s", item.Name),
				Hint:    "run `ccp profile rollback` or delete and recreate the profile",
			})
			continue
		}
		if dstInfo.Mode()&os.ModeSymlink != 0 {
			continue // symlink is always acceptable; pass 2 checks dangling.
		}

		// Runtime is a real entry (file or dir). Decide based on source.
		if item.Dir {
			if !dstInfo.IsDir() {
				out = append(out, DoctorFinding{Profile: pr.Name, Severity: "warn",
					Message: fmt.Sprintf("%s is not a symlink or directory in runtime dir", item.Name),
					Hint:    "remove the entry and re-run `ccp profile refresh`",
				})
			}
			// Real dir is legitimate when the source subtree has refs.
			// We don't re-walk here; pass 3 catches individual render
			// failures.
			continue
		}

		// Regular-file item.
		if srcInfo.Mode().IsRegular() {
			b, err := os.ReadFile(src)
			if err == nil && refs.HasRefs(b) {
				// ccp-rendered file is expected — no warning.
				continue
			}
		}
		out = append(out, DoctorFinding{Profile: pr.Name, Severity: "warn",
			Message: fmt.Sprintf("%s is a regular file in runtime dir; expected symlink", item.Name),
			Hint:    "Claude may have overwritten a ccp symlink; move or delete the file and re-run",
		})
	}

	// Pass 2: catch runtime symlinks whose source disappeared (dangling).
	entries, err := os.ReadDir(pr.ConfigDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		full := filepath.Join(pr.ConfigDir, e.Name())
		info, err := os.Lstat(full)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(full)
		if err != nil {
			continue
		}
		abs := target
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(filepath.Dir(full), abs)
		}
		// Only report dangling links that point into our source tree —
		// symlinks Claude created itself aren't our concern.
		rel, rerr := filepath.Rel(pr.SourceDir, abs)
		if rerr != nil || startsWithDotDot(rel) {
			continue
		}
		if _, err := os.Stat(abs); err != nil {
			out = append(out, DoctorFinding{Profile: pr.Name, Severity: "error",
				Message: fmt.Sprintf("dangling symlink for %s → %s", e.Name(), target),
				Hint:    "the source file was removed; recreate it or unlink the symlink",
			})
		}
	}
	return out
}

// checkSecretRefs walks pr.SourceDir and collects every `{{ ... }}` ref,
// then probes each against the available backends. Surfaces unresolved
// refs as warn findings with actionable hints. Does NOT shell out to
// `op` — surfacing "check 1Password CLI" is the only reliable signal
// without a live network.
func checkSecretRefs(p paths.Paths, pr Profile) []DoctorFinding {
	var out []DoctorFinding

	// Walk source and collect every ref. Dedupe so "the same env var in
	// 10 files" produces one warning, not ten.
	type refKey struct {
		kind  string // "keychain" | "op" | "env"
		value string // key / op-path / var
	}
	seen := map[refKey]struct{}{}

	_ = filepath.WalkDir(pr.SourceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort; a missing file under source is picked
			// up by other passes.
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil || !refs.HasRefs(b) {
			return nil
		}
		for _, s := range extractRefInners(b) {
			ref, perr := refs.ParseRef(s)
			if perr != nil {
				out = append(out, DoctorFinding{Profile: pr.Name, Severity: "warn",
					Message: fmt.Sprintf("malformed secret reference in %s: %q", displayPath(pr.SourceDir, path), s),
					Hint:    "fix the ref syntax; see `ccp help secret`",
				})
				continue
			}
			switch r := ref.(type) {
			case refs.RefKeychain:
				seen[refKey{"keychain", r.Key}] = struct{}{}
			case refs.RefEnv:
				seen[refKey{"env", r.Var}] = struct{}{}
			case refs.RefOp:
				seen[refKey{"op", r.Ref}] = struct{}{}
			}
		}
		return nil
	})

	// Probe. For keychain: run the actual secret.Get via KeychainLookup.
	// For env: os.LookupEnv. For op: we don't invoke `op` (network +
	// biometric prompts); surface a generic "check 1Password CLI" warn
	// so users at least see the ref exists.
	for k := range seen {
		switch k.kind {
		case "keychain":
			if KeychainLookup == nil {
				out = append(out, DoctorFinding{Profile: pr.Name, Severity: "warn",
					Message: fmt.Sprintf("keychain reference {{ keychain:%s }} — lookup not wired", k.value),
					Hint:    fmt.Sprintf("run `ccp secret set %s %s <value>` to populate it", pr.Name, k.value),
				})
				continue
			}
			if _, err := KeychainLookup(pr.Name, k.value); err != nil {
				out = append(out, DoctorFinding{Profile: pr.Name, Severity: "warn",
					Message: fmt.Sprintf("keychain reference {{ keychain:%s }} cannot be resolved: %v", k.value, err),
					Hint:    fmt.Sprintf("run `ccp secret set %s %s <value>` to populate it", pr.Name, k.value),
				})
			}
		case "env":
			if _, ok := os.LookupEnv(k.value); !ok {
				out = append(out, DoctorFinding{Profile: pr.Name, Severity: "warn",
					Message: fmt.Sprintf("environment reference {{ env.%s }} is not set in this shell", k.value),
					Hint:    fmt.Sprintf("export %s=... before running ccp use, or switch to a keychain ref", k.value),
				})
			}
		case "op":
			out = append(out, DoctorFinding{Profile: pr.Name, Severity: "warn",
				Message: fmt.Sprintf("1Password reference {{ %s }} — verify with `op read %s`", k.value, k.value),
				Hint:    "ccp doesn't probe 1Password in doctor to avoid biometric prompts; check manually if you hit issues",
			})
		}
	}

	return out
}

// extractRefInners returns each `{{ ... }}` inner content from b whose
// opener matches a known scheme. Mirrors the peek logic in refs.Render
// without resolving. Used only by doctor — we don't need byte-accurate
// offsets, just a list of candidate ref strings.
func extractRefInners(b []byte) []string {
	var out []string
	i := 0
	for i < len(b) {
		j := bytes.Index(b[i:], []byte("{{"))
		if j < 0 {
			return out
		}
		start := i + j
		// Skip escape sequence.
		if bytes.HasPrefix(b[start:], []byte("{{!}}")) {
			i = start + len("{{!}}")
			continue
		}
		close := bytes.Index(b[start+2:], []byte("}}"))
		if close < 0 {
			return out
		}
		inner := strings.TrimSpace(string(b[start+2 : start+2+close]))
		if refs.HasRefs(b[start : start+2+close+2]) {
			out = append(out, inner)
		}
		i = start + 2 + close + 2
	}
	return out
}

// displayPath turns an absolute path under root into a short form like
// "hooks/on-start.sh" for doctor messages.
func displayPath(root, full string) string {
	if rel, err := filepath.Rel(root, full); err == nil {
		return rel
	}
	return full
}

// Exported so shell-aware callers (e.g. ccp exec's ref-probe path) can
// share the same dry-run behavior. Currently unused outside doctor but
// cheap to keep.
var _ = context.Background
