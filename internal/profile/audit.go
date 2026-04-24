package profile

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dalley/ccp/internal/paths"
)

// AuditFinding is one suspected secret surfaced by Audit. Findings are
// stable-sorted (File ASC, Line ASC) so `--json` output diffs cleanly.
type AuditFinding struct {
	Profile string `json:"profile"`
	// File is the path relative to the profile source directory. For the
	// informational "skipped-large" / "skipped-binary" findings this is
	// still the relative file path.
	File string `json:"file"`
	// Line is 1-based. Zero for whole-file findings (skipped-large).
	Line int `json:"line"`
	// Kind is the detector that matched: one of
	// "aws", "github", "stripe", "slack", "google", "jwt", "pem",
	// "entropy", "skipped-large", "skipped-binary".
	Kind string `json:"kind"`
	// Preview is redacted: first 4 + "…" + last 4 chars of the match.
	// Short matches (<=8 chars) are emitted verbatim — but every detector
	// has a minimum length ≥ 20, so that branch is effectively unused for
	// real findings. For informational skip findings Preview carries a
	// human explanation (e.g. "file size 1572864").
	Preview string `json:"preview"`
}

// auditMaxFileSize is the ceiling above which a file is skipped outright.
// Large vendored blobs, compiled node_modules, minified JS, etc. produce
// huge false-positive entropy hits and would dominate scan time. Users who
// genuinely keep a secret inside a 2MB file can still be flagged by the
// structural regexes if they grep manually — audit is a helper, not a gate.
const auditMaxFileSize = 1 << 20 // 1 MiB

// auditMinEntropyLen is the lower bound on substring length considered by
// the entropy pass. Raised from 20 to 30 per Decision #19 to cut UUID
// (36-char, ~3.2 entropy) false positives.
const auditMinEntropyLen = 30

// auditEntropyThreshold is the Shannon-entropy cutoff for the confirmation
// pass on base64-alphabet substrings. Raised from 3.5 to 4.0 per
// Decision #19 — matches gitleaks's base64-segment heuristic. UUIDs sit
// around 3.2; genuine random base64 blobs sit above 4.5.
const auditEntropyThreshold = 4.0

// auditEntropyThresholdHex is the cutoff for pure-hex substrings. The
// theoretical max entropy for 16 symbols is log2(16)=4.0, which real
// random hashes approach but rarely reach (observed SHA-256 hashes score
// 3.6-3.9). We apply 3.5 to hex so genuine 64-char git object IDs and
// 40-char SHA-1 hashes land in the findings list. UUIDs are still
// excluded because they include dashes — entropyRunRe stops at the
// dash, so the longest pure-hex run is only 12 chars (below the
// 30-char length minimum).
const auditEntropyThresholdHex = 3.5

// auditIgnoreMarker lets users suppress a single-line false positive
// without disabling the whole file.
const auditIgnoreMarker = "# ccp:audit-ignore"

// CountReal returns the number of findings that represent suspected
// secrets (i.e. not informational skip entries like "skipped-large" /
// "skipped-binary"). Callers use this to decide whether to fire
// ErrAuditSecretsDetected — a binary blob or an oversized vendored file
// is a notice, not a conflict.
//
// Single canonical helper; both the CLI audit command and the Export
// path consume it so the skip-kind list lives in exactly one place.
func CountReal(findings []AuditFinding) int {
	n := 0
	for _, f := range findings {
		switch f.Kind {
		case "skipped-large", "skipped-binary":
			// info-level, not a real finding
		default:
			n++
		}
	}
	return n
}

// escapedRefPrefix marks the literal-escape form `{{!}}{{ ... }}` — a
// line that contains this is not flagged even if the bytes inside the
// escape would otherwise match a detector (the user is demonstrating
// syntax, not storing a secret).
const escapedRefPrefix = "{{!}}{{"

// Structural detectors. Each entry is high-precision — a hit emits a
// finding directly, without running the entropy pass on the same line.
//
// Patterns are anchored with \b so they don't spuriously match inside a
// longer identifier. The boundary handling for PEM is special (whole-line
// match is fine since a PEM header occupies a full line).
type structuralDetector struct {
	Kind string
	Re   *regexp.Regexp
}

var structuralDetectors = []structuralDetector{
	// AWS access-key IDs. Prefixes per IAM docs: AKIA (standard), ASIA
	// (temporary/STS), ABIA (bearer), ACCA (context-specific), A3T… used
	// by the canonical test vector AKIAIOSFODNN7EXAMPLE.
	{Kind: "aws", Re: regexp.MustCompile(`\b((?:A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)[A-Z2-7]{16})\b`)},
	// GitHub classic PATs + OAuth tokens: ghp_, gho_, ghu_, ghs_, ghr_
	// (plus GitHub App server tokens which share the prefix family).
	{Kind: "github", Re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36}\b`)},
	// GitHub fine-grained PATs: github_pat_<22>_<59>.
	{Kind: "github", Re: regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9]{22}_[A-Za-z0-9]{59}\b`)},
	// Stripe restricted / secret keys: sk_/rk_ with a test/live/prod env
	// segment, then 10-99 chars of payload. Lenient on the payload length
	// because Stripe has lengthened keys twice since 2020.
	{Kind: "stripe", Re: regexp.MustCompile(`\b(?:sk|rk)_(?:test|live|prod)_[A-Za-z0-9]{10,99}\b`)},
	// Slack tokens: xoxa-, xoxb-, xoxp-, xoxr-, xoxs-.
	{Kind: "slack", Re: regexp.MustCompile(`xox[abprs]-[A-Za-z0-9-]{10,48}`)},
	// Google API keys (per google's docs): AIza + 35 chars of base64url.
	{Kind: "google", Re: regexp.MustCompile(`\bAIza[\w-]{35}\b`)},
	// JWT: three base64url segments separated by dots, first segment
	// starts with "eyJ" ("{" base64url-encoded).
	{Kind: "jwt", Re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)},
	// PEM private key header — covers RSA, DSA, EC, OpenSSH, PGP, and
	// the unqualified "PRIVATE KEY" form (PKCS#8).
	{Kind: "pem", Re: regexp.MustCompile(`-----BEGIN (?:RSA |OPENSSH |DSA |EC |PGP |)PRIVATE KEY-----`)},
}

// entropyRunRe extracts candidate substrings for the Shannon-entropy
// pass. The charset covers base64 (including URL-safe) and hex. We
// deliberately match a single run — if a line has multiple candidate
// runs separated by non-charset chars (spaces, quotes), each is scored
// independently and only one finding per line is emitted.
var entropyRunRe = regexp.MustCompile(`[A-Za-z0-9+/=_-]{30,}`)

// hexOnlyRe classifies a substring as hex for the charset test. Used
// alongside a base64-ish charset check so short hex strings (32-char
// md5, 40-char sha1, 64-char sha256) are still in scope for entropy.
var hexOnlyRe = regexp.MustCompile(`^[A-Fa-f0-9]+$`)

// lineHasSafeRef reports whether the line contains a recognized ref
// scheme — `{{ keychain:... }}`, `{{ op://... }}`, `{{ env.NAME }}` —
// that ccp already treats as a safe indirection. Those lines are NOT
// scanned (the secret itself lives in the keychain / 1Password / env,
// not in the file).
//
// Also matches the literal-escape form `{{!}}{{ ... }}`: users who
// escape a ref are demonstrating syntax, not inlining a secret.
func lineHasSafeRef(line string) bool {
	if strings.Contains(line, escapedRefPrefix) {
		return true
	}
	// Three cheap substring probes are faster than a regex for the
	// common case (most lines are short and contain none of these).
	return strings.Contains(line, "{{ keychain:") ||
		strings.Contains(line, "{{keychain:") ||
		strings.Contains(line, "{{ op://") ||
		strings.Contains(line, "{{op://") ||
		strings.Contains(line, "{{ env.") ||
		strings.Contains(line, "{{env.")
}

// Audit walks a profile's source tree and flags high-probability secret
// patterns. Returns findings sorted by (File, Line, Kind) — a nil slice
// with nil err means the profile is clean.
//
// Returns ErrNotFound if the profile doesn't exist. Transient I/O errors
// mid-walk are returned as-is; the caller decides whether to escalate.
func Audit(p paths.Paths, name string) ([]AuditFinding, error) {
	pr, err := NewChecked(p, name)
	if err != nil {
		return nil, err
	}
	if !pr.Exists() {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	var findings []AuditFinding
	walkErr := filepath.WalkDir(pr.SourceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Skip symlinks that escape the source tree — mirrors the
		// defense in diff.go / profile.go. A symlink pointing at
		// /etc/shadow would otherwise get audited (and its content
		// leaked into the preview field).
		if d.Type()&fs.ModeSymlink != 0 {
			linkTarget, lerr := os.Readlink(path)
			if lerr != nil {
				return nil
			}
			if !symlinkWithin(path, linkTarget, pr.SourceDir) {
				return nil
			}
			// Follow the link for the subsequent stat/read by
			// falling through — filepath.WalkDir with a symlink
			// hands us the link, not its target, so we need an
			// explicit stat.
			info, serr := os.Stat(path)
			if serr != nil || !info.Mode().IsRegular() {
				return nil
			}
		} else if !d.Type().IsRegular() {
			return nil
		}

		rel, rerr := filepath.Rel(pr.SourceDir, path)
		if rerr != nil {
			return rerr
		}

		info, ierr := os.Stat(path)
		if ierr != nil {
			return ierr
		}

		if info.Size() > auditMaxFileSize {
			findings = append(findings, AuditFinding{
				Profile: pr.Name, File: rel, Line: 0,
				Kind:    "skipped-large",
				Preview: fmt.Sprintf("file size %d bytes exceeds %d-byte audit ceiling", info.Size(), auditMaxFileSize),
			})
			return nil
		}

		fileFindings, ferr := auditFile(pr.Name, path, rel)
		if ferr != nil {
			return ferr
		}
		findings = append(findings, fileFindings...)
		return nil
	})
	if walkErr != nil {
		return findings, walkErr
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Kind < findings[j].Kind
	})

	return findings, nil
}

// auditFile scans one file — null-byte sniff, then per-line detector
// cascade. Returns findings tagged with profile + relative path.
func auditFile(profileName, absPath, relPath string) ([]AuditFinding, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Binary-detection heuristic: peek the first 512 bytes (git's cut)
	// and bail if any NUL is present. Cheap, conservative, handles the
	// common cases (images, compiled binaries, pdf). A finding is
	// emitted so users know why a file wasn't scanned — silent skip
	// would make bugs invisible.
	head := make([]byte, 512)
	n, rerr := f.Read(head)
	if rerr != nil && n == 0 {
		return nil, rerr
	}
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return []AuditFinding{{
			Profile: profileName, File: relPath, Line: 0,
			Kind:    "skipped-binary",
			Preview: "file contains NUL bytes in first 512 bytes; treated as binary",
		}}, nil
	}

	// Rewind — bufio.Scanner needs the whole file, head-and-all.
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}

	var out []AuditFinding
	scanner := bufio.NewScanner(f)
	// Allow lines up to 1 MiB — matches auditMaxFileSize so we never
	// truncate a line inside a file that passed the size gate.
	scanner.Buffer(make([]byte, 64*1024), auditMaxFileSize)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Exclusions first — cheap substring probes.
		if lineHasSafeRef(line) {
			continue
		}
		if strings.Contains(line, auditIgnoreMarker) {
			continue
		}

		// Structural pass: emit a finding per matching detector.
		matched := false
		for _, d := range structuralDetectors {
			loc := d.Re.FindStringIndex(line)
			if loc == nil {
				continue
			}
			matched = true
			out = append(out, AuditFinding{
				Profile: profileName, File: relPath, Line: lineNum,
				Kind:    d.Kind,
				Preview: redact(line[loc[0]:loc[1]]),
			})
		}
		if matched {
			// A structural hit on this line is strong evidence —
			// don't also run the entropy pass and emit a duplicate
			// finding for the same substring.
			continue
		}

		// Entropy pass: iterate all qualifying runs.
		for _, loc := range entropyRunRe.FindAllStringIndex(line, -1) {
			sub := line[loc[0]:loc[1]]
			if !entropyCharsetOK(sub) {
				continue
			}
			if len(sub) < auditMinEntropyLen {
				continue
			}
			threshold := auditEntropyThreshold
			if hexOnlyRe.MatchString(sub) {
				threshold = auditEntropyThresholdHex
			}
			if shannonEntropy(sub) < threshold {
				continue
			}
			out = append(out, AuditFinding{
				Profile: profileName, File: relPath, Line: lineNum,
				Kind:    "entropy",
				Preview: redact(sub),
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// entropyCharsetOK is the charset filter described in Decision #19: a
// candidate substring qualifies for the entropy pass if it is pure hex
// (case-insensitive) OR is drawn from the base64/base64url alphabet
// (letters + digits + `+/=_-`). entropyRunRe already filters to the
// base64-ish charset, so this function is mostly confirming the "hex
// sub-case" and rejecting mixed runs that slipped through.
func entropyCharsetOK(s string) bool {
	// The run regex already restricts to base64-ish characters, so
	// this function's main job is to accept the hex sub-case AND to
	// reject obvious non-secrets like long ASCII words (which the run
	// regex would also pass). We accept if the string looks like hex
	// OR if it contains at least one digit (real base64 secrets do).
	if hexOnlyRe.MatchString(s) {
		return true
	}
	hasDigit := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			hasDigit = true
			break
		}
	}
	// A 30-char all-letters string ("aaaaaaaa...") would otherwise
	// score high entropy if the letters varied; rejecting the
	// no-digit case cuts false positives on long dictionary words
	// and template placeholders.
	return hasDigit
}

// shannonEntropy computes H(X) in bits-per-char over the given string.
// Standard definition: -sum(p * log2 p) across byte frequencies.
func shannonEntropy(s string) float64 {
	if len(s) == 0 {
		return 0
	}
	var freq [256]int
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	n := float64(len(s))
	h := 0.0
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}

// redact returns a preview that leaks at most 8 source characters: the
// first 4, an ellipsis, and the last 4. Strings shorter than 9 chars are
// returned verbatim (in practice unreachable given the ≥30 min length).
func redact(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:4] + "…" + s[len(s)-4:]
}
