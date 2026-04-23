// Package refs parses and resolves the `{{ ... }}` template references
// that profile files may contain. Supported schemes:
//
//   - {{ keychain:KEY }}             → current-profile keychain lookup
//   - {{ op://<vault>/<item>/<field> }} → 1Password CLI
//   - {{ env.VAR }}                  → os.LookupEnv
//
// The escape sequence `{{!}}` immediately preceding a ref makes the next
// `{{ ... }}` literal in the rendered output.
//
// This package is not functional on Windows in v2.0 — the CLI layer
// unregisters commands that depend on it. The sentinel below exists so
// ExitCodeFor compiles uniformly.
package refs

import "errors"

// ErrSecretRefUnresolved is returned by Render when a ref cannot be
// resolved: missing keychain entry, unreachable `op`, unset env var, or
// 1Password CLI prompting interactively in a non-TTY context. The error
// message names the offending ref. Maps to ExitState — it is a machine-
// state problem, not user error (the user asked ccp to use a profile whose
// runtime environment doesn't have the necessary secret).
var ErrSecretRefUnresolved = errors.New("secret reference could not be resolved")

// ErrUnsupportedPlatform is returned on Windows. See secret.ErrUnsupportedPlatform.
var ErrUnsupportedPlatform = errors.New("refs resolution is not available on this platform")
