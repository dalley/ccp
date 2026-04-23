package cli

import (
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
	Existed  bool
}

func loadState() (state, error) {
	p, err := paths.Resolve()
	if err != nil {
		return state{}, err
	}
	if err := p.Ensure(); err != nil {
		return state{}, err
	}
	m, existed, err := manifest.Load(p.ManifestPath)
	if err != nil {
		return state{}, err
	}
	return state{Paths: p, Manifest: m, Existed: existed}, nil
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

// saveManifest writes the manifest back to disk under the lock.
func saveManifest(s state) error {
	return manifest.Save(s.Paths.ManifestPath, s.Manifest)
}
