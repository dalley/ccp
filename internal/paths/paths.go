// Package paths resolves the filesystem locations ccp reads and writes.
//
// All public functions respect the CCP_ROOT environment variable as a test
// hook: when set, it overrides $HOME for every derived path, which lets the
// end-to-end tests operate on a throw-away tree without touching the real
// user environment.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Paths holds the resolved filesystem layout for one ccp invocation.
type Paths struct {
	Home               string // $HOME (or CCP_ROOT when set)
	ConfigDir          string // ~/.config/ccp (XDG-aware)
	ProfilesDir        string // <ConfigDir>/profiles
	BackupsDir         string // <ConfigDir>/backups
	SecretsDir         string // <ConfigDir>/secrets — file-fallback store for per-profile keychain entries
	RuntimeManifestDir string // <ConfigDir>/runtime-manifest — tracks which runtime files ccp rendered (vs symlinked or Claude-created)
	ManifestPath       string // <ConfigDir>/manifest.toml
	AllowlistPath      string // <ConfigDir>/allowlist.toml — per-machine .claude-profile approval state
	LockPath           string // <ConfigDir>/lock
	ClaudeHome         string // ~/.claude — Claude's default config directory
}

// Resolve builds a Paths from the current environment. It does not create
// any directories; call Ensure() for that.
func Resolve() (Paths, error) {
	home, err := resolveHome()
	if err != nil {
		return Paths{}, err
	}

	config, err := resolveConfigDir(home)
	if err != nil {
		return Paths{}, err
	}

	return Paths{
		Home:               home,
		ConfigDir:          config,
		ProfilesDir:        filepath.Join(config, "profiles"),
		BackupsDir:         filepath.Join(config, "backups"),
		SecretsDir:         filepath.Join(config, "secrets"),
		RuntimeManifestDir: filepath.Join(config, "runtime-manifest"),
		ManifestPath:       filepath.Join(config, "manifest.toml"),
		AllowlistPath:      filepath.Join(config, "allowlist.toml"),
		LockPath:           filepath.Join(config, "lock"),
		ClaudeHome:         filepath.Join(home, ".claude"),
	}, nil
}

// ProfileSourceDir returns where the non-runtime source files for a profile
// live (the Git-synced tree).
func (p Paths) ProfileSourceDir(name string) string {
	return filepath.Join(p.ProfilesDir, name)
}

// ProfileConfigDir returns the CLAUDE_CONFIG_DIR used at runtime for a profile.
// This is where Claude Code writes auth tokens, session files, caches, etc.
func (p Paths) ProfileConfigDir(name string) string {
	return filepath.Join(p.Home, ".claude-"+name)
}

// SecretFilePath returns the on-disk path of the file-fallback secret store
// for the named profile. The file may or may not exist; presence is load-
// bearing (secrets have been written via the fallback path).
func (p Paths) SecretFilePath(name string) string {
	return filepath.Join(p.SecretsDir, name+".json")
}

// RuntimeManifestPath returns the on-disk path of the runtime manifest that
// tracks which files ccp has rendered into a profile's runtime dir. Used by
// BuildSymlinks / RefreshSymlinks to distinguish ccp-owned regular files
// from Claude-created content.
func (p Paths) RuntimeManifestPath(name string) string {
	return filepath.Join(p.RuntimeManifestDir, name+".json")
}

// Ensure creates the persistent ccp directories if they don't yet exist.
// Safe to call repeatedly.
func (p Paths) Ensure() error {
	for _, d := range []string{p.ConfigDir, p.ProfilesDir, p.BackupsDir, p.SecretsDir, p.RuntimeManifestDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

// ExpandHome replaces a leading ~ in p with the resolved home directory.
func (p Paths) ExpandHome(path string) string {
	if path == "~" {
		return p.Home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(p.Home, path[2:])
	}
	return path
}

// ToHomeRelative replaces a leading <home>/ with ~/ for display / storage.
// Always uses $HOME as the anchor, even when Paths was resolved from CCP_ROOT.
func (p Paths) ToHomeRelative(path string) string {
	if path == p.Home {
		return "~"
	}
	if rel, err := filepath.Rel(p.Home, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return path
}

func resolveHome() (string, error) {
	if v := os.Getenv("CCP_ROOT"); v != "" {
		abs, err := filepath.Abs(v)
		if err != nil {
			return "", fmt.Errorf("expand CCP_ROOT: %w", err)
		}
		return abs, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return home, nil
}

func resolveConfigDir(home string) (string, error) {
	// If CCP_ROOT is set, ccp's config is rooted under it rather than the
	// real XDG location — keeps tests hermetic.
	if os.Getenv("CCP_ROOT") != "" {
		return filepath.Join(home, ".config", "ccp"), nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "ccp"), nil
	}
	return filepath.Join(home, ".config", "ccp"), nil
}
