package cli

import (
	"errors"
	"fmt"

	"github.com/dalley/ccp/internal/paths"
	"github.com/dalley/ccp/internal/profile"
	"github.com/dalley/ccp/internal/refs"
	"github.com/dalley/ccp/internal/secret"
)

// init wires profile.KeychainLookup to secret.Get. The profile package
// can't import secret directly because secret already depends on
// profile — this hook breaks the cycle.
//
// Errors from secret.Get are wrapped with refs.ErrSecretRefUnresolved so
// callers that check `errors.Is(err, refs.ErrSecretRefUnresolved)` (e.g.
// the exit-code classifier) see the right sentinel regardless of which
// backend produced the miss.
func init() {
	profile.KeychainLookup = func(prof, key string) (string, error) {
		p, err := paths.Resolve()
		if err != nil {
			return "", fmt.Errorf("resolve paths: %w", err)
		}
		v, err := secret.Get(p, prof, key)
		if err != nil {
			if errors.Is(err, secret.ErrSecretNotFound) {
				return "", fmt.Errorf("keychain key %q not found for profile %q: %w",
					key, prof, refs.ErrSecretRefUnresolved)
			}
			return "", err
		}
		return v, nil
	}
}
