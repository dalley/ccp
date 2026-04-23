// Package secret provides per-profile secret storage with an OS-keychain-
// first, file-fallback backend. Values are referenced from profile files via
// the `{{ keychain:KEY }}` template syntax resolved in internal/refs.
//
// The package is not registered on Windows in v2.0 — the CLI layer
// unregisters the `ccp secret` commands under GOOS=windows. The sentinels
// defined here are platform-independent so the CLI's ExitCodeFor can still
// compile and classify them everywhere.
package secret

import "errors"

// ErrSecretNotFound is returned by Get/Delete when the requested key is
// present in neither the OS keychain nor the file-fallback store. Maps to
// ExitUser: the caller asked for something that does not exist.
var ErrSecretNotFound = errors.New("secret not found")

// ErrKeychainLocked is returned by Get/Set/Delete when the OS keychain is
// reachable but access is denied (macOS keychain locked, Linux Secret
// Service locked). Maps to ExitState: the user must unlock their keychain
// and retry. DISTINCT from ErrSecretNotFound so agents don't misdiagnose a
// locked keychain as a missing key.
var ErrKeychainLocked = errors.New("keychain is locked; unlock and retry")

// ErrKeychainUnavailable is returned by Set when no keychain backend is
// reachable (headless Linux with no D-Bus, devcontainer, etc.). Internally
// this error triggers the file-fallback write path; callers rarely see it
// directly. Maps to ExitState if it ever leaks.
var ErrKeychainUnavailable = errors.New("OS keychain not available")

// ErrUnsupportedPlatform is returned from the Windows stubs of this
// package. The CLI layer hides `ccp secret` commands on Windows so users
// should never see this directly — it exists as defence-in-depth for
// internal callers that somehow reach a keychain operation on GOOS=windows.
var ErrUnsupportedPlatform = errors.New("ccp secret is not available on this platform")
