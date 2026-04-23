//go:build windows

// Package refs: Windows stub.
//
// v2.0 does not support secret references on Windows — the CLI layer
// unregisters the commands that depend on this package, and the few
// call sites that remain (doctor, exec's HasAnyRefs probe) are guarded
// against ErrUnsupportedPlatform.
//
// We still need the package to compile on GOOS=windows so cross-builds
// work, hence these stubs. Every public entry point returns
// ErrUnsupportedPlatform. HasRefs returns false so Render's short-circuit
// behavior works even if some caller bypasses the guard.
package refs

import "context"

// Ref tagged union — kept identical to the non-Windows build so code in
// other packages that references these types still compiles.

type Ref interface{ isRef() }

type RefKeychain struct{ Key string }

type RefOp struct{ Ref string }

type RefEnv struct{ Var string }

func (RefKeychain) isRef() {}
func (RefOp) isRef()       {}
func (RefEnv) isRef()      {}

// Resolver mirrors the non-Windows interface so consumers compile.
type Resolver interface {
	Resolve(ctx context.Context, ref Ref) (string, error)
}

// DefaultResolver is a compile-compatible shell; every method returns
// ErrUnsupportedPlatform.
type DefaultResolver struct {
	Profile    string
	KeyringGet func(service, account, key string) (string, error)
	EnvLookup  func(name string) (string, bool)
}

// Resolve always returns ErrUnsupportedPlatform on Windows.
func (DefaultResolver) Resolve(_ context.Context, _ Ref) (string, error) {
	return "", ErrUnsupportedPlatform
}

// HasRefs returns false on Windows — the CLI layer hides ref-dependent
// commands so this path is effectively cold, but returning false also
// lets Render short-circuit cleanly if anything does call in.
func HasRefs(_ []byte) bool { return false }

// HasAnyRefs returns ErrUnsupportedPlatform on Windows.
func HasAnyRefs(_ string) (bool, error) {
	return false, ErrUnsupportedPlatform
}

// Render returns ErrUnsupportedPlatform on Windows.
func Render(_ context.Context, _ []byte, _ Resolver) ([]byte, error) {
	return nil, ErrUnsupportedPlatform
}

// ParseRef returns ErrUnsupportedPlatform on Windows.
func ParseRef(_ string) (Ref, error) {
	return nil, ErrUnsupportedPlatform
}

// SetOpRead is a no-op on Windows; included for API parity.
func SetOpRead(_ func(ctx context.Context, ref string) (string, error)) {}
