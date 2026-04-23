// Package allowlist stores direnv-style approval state for auto-activation
// `.claude-profile` markers. Each entry maps a marker's absolute path to a
// SHA-256 of its contents; auto-activation only sets CLAUDE_CONFIG_DIR
// when the current contents hash matches an approved entry (fail-closed).
//
// The file `~/.config/ccp/allowlist.toml` is per-machine trust state —
// gitignored and never synced across machines. See the v2.0 plan for why
// content-only hashing is used (vs direnv's path+content scheme).
package allowlist

import "errors"

// ErrMarkerNotAllowed is returned when an operation requires an approved
// marker at a path but none is recorded. Maps to ExitConflict — the user
// must run `ccp allow` to proceed.
var ErrMarkerNotAllowed = errors.New("marker is not allowed; run `ccp allow` to approve it")

// ErrMarkerHashMismatch is returned when a marker's current contents do
// not match the recorded hash. Indicates the file has been edited since
// approval (benign, user intent) or substituted (hostile commit). Maps to
// ExitConflict; fail-closed, user must `ccp allow` again if intended.
var ErrMarkerHashMismatch = errors.New("marker content has changed since it was allowed")

// ErrInvalidMarker is returned by ReadName when the marker does not match
// the strict single-line format (profile name regex, optional trailing
// newline, no BOM/CRLF/extra whitespace). The error wraps byte-offset
// context so the user can find the offending character.
var ErrInvalidMarker = errors.New("invalid .claude-profile marker format")

// ErrUnsupportedPlatform is returned on Windows. See secret.ErrUnsupportedPlatform.
var ErrUnsupportedPlatform = errors.New("allow-list is not available on this platform")
