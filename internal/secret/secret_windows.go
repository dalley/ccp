//go:build windows

package secret

// Windows is not supported in v2.0. The CLI layer hides `ccp secret`
// commands under GOOS=windows (Unit 5); these stubs exist so any
// internal caller that reaches this package on Windows returns the typed
// ErrUnsupportedPlatform sentinel instead of panicking in syscall-land.

import (
	"io"

	"github.com/dalley/ccp/internal/paths"
)

// SetFallbackWarnWriter is a no-op on Windows — there is no fallback
// warning because the public API always returns ErrUnsupportedPlatform
// before any I/O happens. Exists to keep the cross-platform package shape
// identical so importers compile cleanly under GOOS=windows.
func SetFallbackWarnWriter(_ io.Writer) {}

// ValidateKey preserves the POSIX shape so callers that validate input
// before a Set/Get (e.g., early CLI argument validation) compile and run
// the same on Windows.
func ValidateKey(_ string) error {
	return ErrUnsupportedPlatform
}

func Set(_ paths.Paths, _, _, _ string) error {
	return ErrUnsupportedPlatform
}

func Get(_ paths.Paths, _, _ string) (string, error) {
	return "", ErrUnsupportedPlatform
}

func List(_ paths.Paths, _ string) ([]string, error) {
	return nil, ErrUnsupportedPlatform
}

func Delete(_ paths.Paths, _, _ string) error {
	return ErrUnsupportedPlatform
}
