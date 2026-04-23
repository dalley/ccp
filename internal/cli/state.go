package cli

import (
	"fmt"

	"github.com/dalley/ccp/internal/fslock"
	"github.com/dalley/ccp/internal/manifest"
	"github.com/dalley/ccp/internal/paths"
)

// state bundles the filesystem locations and the loaded manifest — the two
// things every ccp command touches. Loading them in one helper keeps command
// implementations short and consistent.
type state struct {
	Paths    paths.Paths
	Manifest manifest.Manifest
}

func loadState() (state, error) {
	p, err := paths.Resolve()
	if err != nil {
		return state{}, err
	}
	if err := p.Ensure(); err != nil {
		return state{}, err
	}
	m, _, err := manifest.Load(p.ManifestPath)
	if err != nil {
		return state{}, err
	}
	return state{Paths: p, Manifest: m}, nil
}

// withLock acquires the global ccp state lock, runs fn, and releases.
func withLock(p paths.Paths, fn func() error) error {
	l, err := fslock.Acquire(p.LockPath)
	if err != nil {
		return err
	}
	defer func() { _ = l.Release() }()
	return fn()
}

// withLockedState acquires the lock, reloads the manifest INSIDE the lock
// (so we never operate on a manifest that was stale by the time we started
// mutating), runs fn with the fresh state, and saves the manifest after.
//
// Commands that intend to mutate the manifest must use this helper rather
// than `loadState` + `withLock` — the latter has a TOCTOU window between
// load and lock-acquisition that lets concurrent writers overwrite each
// other silently.
//
// Save is non-atomic with respect to fn's filesystem mutations — if fn
// commits a rename/delete and manifest.Save then fails (out-of-space,
// permission), the on-disk state diverges from the manifest. We surface
// that divergence loudly rather than silently; truly atomic two-phase
// commit would require each caller to produce an inverse operation and is
// out of scope.
func withLockedState(p paths.Paths, fn func(s *state) error) error {
	return withLock(p, func() error {
		m, _, err := manifest.Load(p.ManifestPath)
		if err != nil {
			return err
		}
		s := &state{Paths: p, Manifest: m}
		if err := fn(s); err != nil {
			return err
		}
		if err := manifest.Save(p.ManifestPath, s.Manifest); err != nil {
			return fmt.Errorf("filesystem changes committed but manifest save failed — "+
				"inspect ~/.config/ccp/manifest.toml and re-run the command to repair: %w", err)
		}
		return nil
	})
}
