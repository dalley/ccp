package profile

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dalley/ccp/internal/paths"
	"github.com/dalley/ccp/internal/refs"
)

// ExportSchemaVersion is the integer written into EXPORT_MANIFEST.json.
// Bumped whenever the manifest shape changes incompatibly; a future
// `ccp profile import` should refuse unknown versions.
const ExportSchemaVersion = 1

// ExportManifestName is the reserved filename at the top of every export
// tar. Placing the manifest first means a streaming extractor can gate
// further processing on it (e.g. prompt the user when contains_secrets is
// true) before reading any profile bytes.
const ExportManifestName = "EXPORT_MANIFEST.json"

// ExportOptions controls the behavior of Export.
type ExportOptions struct {
	// IncludeSecrets resolves every ref via refs.Render and inlines the
	// resolved value into the tarball. Also includes the per-profile
	// secrets file verbatim at secrets/<name>.json. Callers MUST gate this
	// behind a TTY confirmation path (see internal/cli/profile_export.go)
	// — this package does not prompt.
	IncludeSecrets bool
	// FailOnAudit runs profile.Audit and aborts the export with
	// ErrAuditSecretsDetected if any real findings exist. Opt-in: the
	// default posture is "advisory", because the entropy detector
	// produces false positives and we don't want to block legitimate
	// exports on a UUID or git hash.
	FailOnAudit bool
	// SkipAudit disables the audit scan entirely. Takes precedence over
	// FailOnAudit (defensive: if both are set by a caller bug, we skip
	// rather than fail).
	SkipAudit bool
	// Hostname, if non-empty, overrides os.Hostname() in the manifest.
	// Tests pin this to keep golden-file assertions deterministic.
	Hostname string
	// Now, if non-zero, overrides time.Now() in the manifest. Tests pin
	// this so exported_at is reproducible.
	Now time.Time
	// AuditAdvisory, if non-nil, receives the audit findings when the
	// scan ran in advisory mode (no FailOnAudit). Callers use this to
	// print a single-line hint to stderr without re-running Audit. Nil
	// means "caller doesn't care about advisory findings".
	AuditAdvisory *[]AuditFinding
	// Resolver, if non-nil and IncludeSecrets is true, resolves refs
	// during tar emission. Injected by the CLI (which wires it to
	// secret.Get) to avoid an import cycle — internal/secret already
	// imports internal/profile, so profile can't import it back.
	//
	// When IncludeSecrets is true and Resolver is nil, Export uses a
	// zero-value refs.DefaultResolver with the profile name filled in
	// — which returns ErrSecretRefUnresolved for every keychain ref,
	// giving a clear error rather than silently emitting the
	// unrendered ref syntax.
	Resolver refs.Resolver
}

// exportManifest is the JSON shape serialized as EXPORT_MANIFEST.json.
// Field ordering in the encoded output follows Go struct declaration
// order so a roundtrip diff is stable.
type exportManifest struct {
	SchemaVersion    int      `json:"schema_version"`
	Profile          string   `json:"profile"`
	ExportedAt       string   `json:"exported_at"`
	ExporterHostname string   `json:"exporter_hostname"`
	ContainsSecrets  bool     `json:"contains_secrets"`
	Files            []string `json:"files"`
	// InlinedFiles lists the relative paths whose {{ ref }} tokens were
	// resolved (i.e. they contained refs and IncludeSecrets=true). Only
	// populated when ContainsSecrets is true. A file listed here is NOT
	// guaranteed to contain cleartext secrets — if a ref resolved to an
	// empty string, the "inlined" label is still accurate: refs were
	// materialized out. This matters for importers that want to warn
	// "these files had secrets stripped from their template form".
	InlinedFiles []string `json:"inlined_files,omitempty"`
}

// Export streams a portable tar archive of the profile to w. Honors
// opts.IncludeSecrets for ref resolution, opts.FailOnAudit / SkipAudit
// for the audit gate, and writes EXPORT_MANIFEST.json as the first entry.
//
// The tar layout is:
//   - EXPORT_MANIFEST.json (always first)
//   - <relpath>… for every regular file under the profile's SourceDir
//   - secrets/<name>.json (only when IncludeSecrets is true AND the
//     per-profile secrets file exists on disk)
//
// Symlinks whose target escapes the source tree are refused mid-stream
// (matches mergeCopyDir's defense — otherwise a hostile `git clone` of
// a profile could exfiltrate /etc/shadow on export).
//
// Errors from audit (when FailOnAudit) and ref resolution (when
// IncludeSecrets) are returned directly so the CLI layer can map them
// to the right exit code via ExitCodeFor.
func Export(p paths.Paths, name string, opts ExportOptions, w io.Writer) error {
	pr, err := NewChecked(p, name)
	if err != nil {
		return err
	}
	exists, err := pr.ExistsErr()
	if err != nil {
		return fmt.Errorf("check profile %s: %w", name, err)
	}
	if !exists {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	// Audit gate. SkipAudit wins over FailOnAudit by design — a caller
	// that sets both (argparse bug, forward-compat shim) should get the
	// less-surprising "don't scan" behavior rather than a conflict error.
	var findings []AuditFinding
	if !opts.SkipAudit {
		findings, err = Audit(p, name)
		if err != nil {
			return err
		}
		if opts.FailOnAudit && CountReal(findings) > 0 {
			return fmt.Errorf("%w: %d suspected secret(s) in profile %s",
				ErrAuditSecretsDetected, CountReal(findings), name)
		}
		if opts.AuditAdvisory != nil {
			*opts.AuditAdvisory = findings
		}
	}

	// Walk the source dir once to collect a stable, sorted file list.
	// We need the list up front (not streamed) because the manifest
	// goes FIRST in the tar — which means we must know every entry
	// before writing the first byte.
	entries, err := collectExportEntries(pr.SourceDir)
	if err != nil {
		return err
	}

	manifest := exportManifest{
		SchemaVersion:    ExportSchemaVersion,
		Profile:          name,
		ExportedAt:       exportTimestamp(opts.Now),
		ExporterHostname: exportHostname(opts.Hostname),
		ContainsSecrets:  opts.IncludeSecrets,
	}
	// Populate the Files list from the collected entries. secrets/<name>.json
	// is appended at write time below so it appears in the manifest only
	// when IncludeSecrets is true AND the file actually exists.
	for _, e := range entries {
		manifest.Files = append(manifest.Files, e.rel)
	}

	// Resolver fallback when the caller didn't inject one. The default
	// has no KeyringGet, which means keychain refs error cleanly via
	// ErrSecretRefUnresolved rather than silently inlining nothing.
	resolver := opts.Resolver
	if opts.IncludeSecrets && resolver == nil {
		resolver = refs.DefaultResolver{Profile: name, EnvLookup: os.LookupEnv}
	}

	// Pre-resolve refs and record which files ended up "inlined" so
	// the manifest lists them. We do this BEFORE starting tar output
	// so a resolution failure surfaces a clean error without emitting
	// a truncated tar.
	renderedBytes := make(map[string][]byte, len(entries))
	if opts.IncludeSecrets {
		ctx := context.Background()
		for _, e := range entries {
			if !e.isRegular {
				continue
			}
			raw, readErr := os.ReadFile(e.abs)
			if readErr != nil {
				return fmt.Errorf("read %s: %w", e.rel, readErr)
			}
			if !refs.HasRefs(raw) {
				renderedBytes[e.rel] = raw
				continue
			}
			out, rerr := refs.Render(ctx, raw, resolver)
			if rerr != nil {
				return rerr
			}
			renderedBytes[e.rel] = out
			manifest.InlinedFiles = append(manifest.InlinedFiles, e.rel)
		}
		sort.Strings(manifest.InlinedFiles)
	}

	// Decide whether secrets/<name>.json ships, and if so, stage it.
	// We stat ONCE here (vs checking inside the tar loop) so the
	// manifest's Files list is accurate — the recipient relies on it
	// to enumerate expected entries without re-reading the tar.
	var secretFileBytes []byte
	var secretFileMode os.FileMode
	if opts.IncludeSecrets {
		secretPath := p.SecretFilePath(name)
		st, sterr := os.Stat(secretPath)
		if sterr == nil && st.Mode().IsRegular() {
			b, rerr := os.ReadFile(secretPath)
			if rerr != nil {
				return fmt.Errorf("read secrets file %s: %w", secretPath, rerr)
			}
			secretFileBytes = b
			secretFileMode = st.Mode().Perm()
			manifest.Files = append(manifest.Files, "secrets/"+name+".json")
		} else if sterr != nil && !os.IsNotExist(sterr) {
			return fmt.Errorf("stat secrets file %s: %w", secretPath, sterr)
		}
		// Re-sort so the appended secrets entry lands alphabetically;
		// consumers comparing Files to a walk of the extracted tar get
		// a stable order.
		sort.Strings(manifest.Files)
	}

	// Tar time. archive/tar's Writer is streaming — we never buffer
	// more than one entry at a time (plus the manifest bytes, which
	// are small).
	tw := tar.NewWriter(w)

	if err := writeManifestEntry(tw, manifest, opts.Now); err != nil {
		_ = tw.Close()
		return err
	}

	// Source-tree entries. Preserves perm bits from the on-disk file
	// so executables stay executable after extraction.
	for _, e := range entries {
		if err := writeSourceEntry(tw, e, renderedBytes, opts.IncludeSecrets); err != nil {
			_ = tw.Close()
			return err
		}
	}

	// secrets/<name>.json, last — by placement convention, not by
	// semantic necessity. Agents that tail the tar to decide whether
	// to re-prompt on import can spot it without reading the manifest.
	if secretFileBytes != nil {
		if err := writeRawEntry(tw, "secrets/"+name+".json", secretFileBytes, secretFileMode, opts.Now); err != nil {
			_ = tw.Close()
			return err
		}
	}

	return tw.Close()
}

// exportEntry is one source-tree file destined for the tar. Collected up
// front (vs discovered streaming) so the manifest can list every entry
// before any file bytes are written.
type exportEntry struct {
	abs       string      // absolute on-disk path
	rel       string      // slash-normalized relative path (tar convention)
	mode      os.FileMode // perm bits only
	size      int64       // used for the tar header when not re-rendered
	mtime     time.Time   // captured at walk time (UTC) so tar headers are deterministic without an extra per-write Stat
	isRegular bool        // false for directories (which we encode as header-only entries) and symlinks
	isSymlink bool
	linkTo    string // relative, in-tree symlink target
}

// collectExportEntries walks the source dir and returns every regular
// file, directory, and in-tree symlink — sorted by rel path so the tar
// is deterministic. Symlinks that escape the source are refused outright
// (fail loud: the user asked for a PORTABLE archive and dangling or
// escaping links defeat that goal).
func collectExportEntries(src string) ([]exportEntry, error) {
	var out []exportEntry
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil // skip the root itself; tar extractors recreate it
		}
		// Slash-normalize — tar headers are always forward-slash, even
		// on Windows producers.
		rel = filepath.ToSlash(rel)

		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}

		// Capture mtime at walk time and normalize to UTC immediately.
		// Previously we statted each entry again at write time — that
		// was both a gratuitous syscall AND a race window (an extract-
		// and-edit-during-export cycle would land an inconsistent
		// mtime in the tar). Walk-time capture pins the value.
		mtime := info.ModTime().UTC()

		switch {
		case d.Type()&fs.ModeSymlink != 0:
			link, lerr := os.Readlink(path)
			if lerr != nil {
				return fmt.Errorf("readlink %s: %w", rel, lerr)
			}
			if !symlinkWithin(path, link, src) {
				return fmt.Errorf("refusing to export symlink %q → %q: target escapes profile source", rel, link)
			}
			out = append(out, exportEntry{
				abs:       path,
				rel:       rel,
				mode:      info.Mode().Perm(),
				mtime:     mtime,
				isSymlink: true,
				linkTo:    filepath.ToSlash(link),
			})
		case d.IsDir():
			out = append(out, exportEntry{
				abs:   path,
				rel:   rel,
				mode:  info.Mode().Perm(),
				mtime: mtime,
			})
		case info.Mode().IsRegular():
			out = append(out, exportEntry{
				abs:       path,
				rel:       rel,
				mode:      info.Mode().Perm(),
				size:      info.Size(),
				mtime:     mtime,
				isRegular: true,
			})
		default:
			// Skip devices, sockets, FIFOs — not portable via tar for
			// our use case. Silent skip is fine here; anyone who plants
			// a FIFO in their profile source tree is doing something
			// ccp doesn't claim to support.
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// writeManifestEntry serializes manifest to JSON and emits it as the
// first tar entry. Manifest goes first so a streaming import can gate
// on contains_secrets before reading any profile bytes.
func writeManifestEntry(tw *tar.Writer, m exportManifest, now time.Time) error {
	buf, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	// Trailing newline for POSIX-tool friendliness — `cat` output of
	// the extracted manifest shouldn't run onto the next shell prompt.
	buf = append(buf, '\n')
	return writeRawEntry(tw, ExportManifestName, buf, 0o644, now)
}

// writeSourceEntry encodes one exportEntry (regular, directory, or
// in-tree symlink) into the tar stream, using pre-rendered bytes from
// renderedBytes when IncludeSecrets is true and the file had refs.
func writeSourceEntry(tw *tar.Writer, e exportEntry, renderedBytes map[string][]byte, includeSecrets bool) error {
	switch {
	case e.isSymlink:
		hdr := &tar.Header{
			Name:     e.rel,
			Mode:     int64(e.mode),
			Typeflag: tar.TypeSymlink,
			Linkname: e.linkTo,
			ModTime:  e.mtime,
			Format:   tar.FormatPAX,
		}
		return tw.WriteHeader(hdr)
	case !e.isRegular:
		// Directory: header-only. Trailing slash is tar convention but
		// archive/tar's TypeDir handles it without us appending.
		hdr := &tar.Header{
			Name:     e.rel + "/",
			Mode:     int64(e.mode),
			Typeflag: tar.TypeDir,
			ModTime:  e.mtime,
			Format:   tar.FormatPAX,
		}
		return tw.WriteHeader(hdr)
	}

	// Regular file.
	var payload []byte
	if includeSecrets {
		// renderedBytes holds the resolved form for every regular file
		// (whether or not it actually had refs) so this lookup is total.
		payload = renderedBytes[e.rel]
	} else {
		b, err := os.ReadFile(e.abs)
		if err != nil {
			return fmt.Errorf("read %s: %w", e.rel, err)
		}
		payload = b
	}
	hdr := &tar.Header{
		Name:     e.rel,
		Mode:     int64(e.mode),
		Size:     int64(len(payload)),
		Typeflag: tar.TypeReg,
		ModTime:  e.mtime,
		Format:   tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header %s: %w", e.rel, err)
	}
	if _, err := io.Copy(tw, bytes.NewReader(payload)); err != nil {
		return fmt.Errorf("write tar body %s: %w", e.rel, err)
	}
	return nil
}

// writeRawEntry emits one in-memory byte slice as a regular tar entry.
// Used for EXPORT_MANIFEST.json and for secrets/<name>.json — both of
// which we hold in memory (they're small).
func writeRawEntry(tw *tar.Writer, name string, payload []byte, mode os.FileMode, now time.Time) error {
	hdr := &tar.Header{
		Name:     name,
		Mode:     int64(mode.Perm()),
		Size:     int64(len(payload)),
		Typeflag: tar.TypeReg,
		ModTime:  nowOrZero(now),
		Format:   tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header %s: %w", name, err)
	}
	if _, err := io.Copy(tw, bytes.NewReader(payload)); err != nil {
		return fmt.Errorf("write tar body %s: %w", name, err)
	}
	return nil
}

// exportTimestamp returns an RFC3339Nano timestamp for the manifest. A
// zero-value override (no Now pinned) uses time.Now().UTC() so the
// manifest timestamp is timezone-stable for users in non-UTC locales.
func exportTimestamp(now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return now.UTC().Format(time.RFC3339)
}

// exportHostname returns the manifest hostname: the override if set,
// else os.Hostname(). A hostname lookup failure degrades to "unknown"
// — the export should still succeed; the hostname is informational.
func exportHostname(override string) string {
	if override != "" {
		return override
	}
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}

// nowOrZero picks now if set, otherwise time.Now().UTC(). Used for the
// manifest + secrets tar entries where we don't have an on-disk mtime.
func nowOrZero(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now.UTC()
}
