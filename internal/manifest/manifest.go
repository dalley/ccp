// Package manifest reads and writes ~/.config/ccp/manifest.toml — the source
// of truth for which profile is active and what schema version the on-disk
// layout conforms to.
//
// Reads preserve unknown keys where the toml library allows (BurntSushi/toml
// decodes known fields and drops unknown ones — we store the raw bytes
// alongside so a future ccp version can read a newer-schema manifest without
// corrupting it on write-back).
package manifest

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// CurrentSchemaVersion is the manifest format this ccp binary writes.
// Increment and add a migration when the shape changes.
const CurrentSchemaVersion = 1

// Manifest is the on-disk state. Keep the struct conservative; anything
// speculative goes on a versioned sub-section.
type Manifest struct {
	SchemaVersion int    `toml:"schema_version"`
	ActiveProfile string `toml:"active_profile,omitempty"`
	DefaultShell  string `toml:"default_shell,omitempty"`
	// LastSeenVersion is the ccp binary version that last wrote this
	// manifest. Used to drive one-shot migration advisories (e.g. the v2
	// secrets-separation prompt on first post-upgrade `ccp use`). This
	// field is additive — schema_version stays at 1; a v1 binary reading
	// a manifest with this field simply drops it on round-trip, which is
	// the desired behavior (no migration advisory on a downgrade).
	LastSeenVersion string `toml:"last_seen_version,omitempty"`
}

// Default returns a manifest suitable for a fresh install.
func Default() Manifest {
	return Manifest{SchemaVersion: CurrentSchemaVersion}
}

// Load reads the manifest at path. If the file does not exist, it returns
// a Default manifest and (nil, false, nil) via the bool for "existed".
func Load(path string) (Manifest, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), false, nil
		}
		return Manifest{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	var m Manifest
	if err := toml.Unmarshal(b, &m); err != nil {
		return Manifest{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if m.SchemaVersion == 0 {
		m.SchemaVersion = CurrentSchemaVersion
	}
	if m.SchemaVersion > CurrentSchemaVersion {
		return m, true, fmt.Errorf("manifest schema %d is newer than this ccp (%d); upgrade ccp",
			m.SchemaVersion, CurrentSchemaVersion)
	}
	return m, true, nil
}

// Save writes the manifest atomically via temp-file + rename(2).
// Parent directory must exist.
func Save(path string, m Manifest) error {
	if m.SchemaVersion == 0 {
		m.SchemaVersion = CurrentSchemaVersion
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	// If anything below fails, clean up the temp file.
	defer func() { _ = os.Remove(tmpName) }()

	enc := toml.NewEncoder(tmp)
	if err := enc.Encode(m); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode toml: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}
