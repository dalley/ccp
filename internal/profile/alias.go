package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// aliasBeginFmt and aliasEndFmt are the marker comments that fence a profile
// alias block in a shellrc. The profile name is inlined so each block is
// self-identifying and deterministically removable.
const (
	aliasBeginFmt = "# >>> ccp profile: %s >>>"
	aliasEndFmt   = "# <<< ccp profile: %s <<<"
)

// AliasBlock renders the alias snippet for a profile.
//
// Uses $HOME so the block is portable across machines with different
// usernames — fixes jean-claude's hard-coded-absolute-path limitation.
func AliasBlock(profileName string) string {
	begin := fmt.Sprintf(aliasBeginFmt, profileName)
	end := fmt.Sprintf(aliasEndFmt, profileName)
	alias := fmt.Sprintf(`alias claude-%[1]s='CLAUDE_CONFIG_DIR="$HOME/.claude-%[1]s" claude'`, profileName)
	return begin + "\n" + alias + "\n" + end + "\n"
}

// InstallAlias appends (or replaces) the alias block for name in shellrcPath.
// Creates the file if missing.
func InstallAlias(shellrcPath, name string) error {
	content, err := readOrEmpty(shellrcPath)
	if err != nil {
		return err
	}
	block := AliasBlock(name)
	// Replace any existing block for this profile, otherwise append.
	if re, _ := blockRegex(name); re.MatchString(content) {
		content = re.ReplaceAllString(content, strings.TrimRight(block, "\n"))
	} else {
		if !strings.HasSuffix(content, "\n") && content != "" {
			content += "\n"
		}
		content += "\n" + block
	}
	return atomicWriteFile(shellrcPath, []byte(content), 0o644)
}

// AliasExists reports whether shellrcPath currently contains an alias block
// for name. Used by commands that want to tell the user whether a removal
// just happened so they can re-install the alias elsewhere.
func AliasExists(shellrcPath, name string) bool {
	content, err := readOrEmpty(shellrcPath)
	if err != nil {
		return false
	}
	re, err := blockRegex(name)
	if err != nil {
		return false
	}
	return re.MatchString(content)
}

// UninstallAlias removes any alias block for name from shellrcPath. Missing
// file or missing block is treated as success.
func UninstallAlias(shellrcPath, name string) error {
	content, err := readOrEmpty(shellrcPath)
	if err != nil {
		return err
	}
	re, _ := blockRegex(name)
	newContent := re.ReplaceAllString(content, "")
	// Collapse the triple-newline left by the removal.
	newContent = strings.ReplaceAll(newContent, "\n\n\n", "\n\n")
	if newContent == content {
		return nil
	}
	return atomicWriteFile(shellrcPath, []byte(newContent), 0o644)
}

// atomicWriteFile writes data to path via a temp file + rename. Preserves
// the target's existing mode; falls back to `mode` when creating a new
// file. Unlike os.WriteFile — which truncates the target before writing —
// this leaves the original intact on any write-phase error (disk full,
// permissions). Critical for shellrc writes: a botched truncate would
// wipe the user's shell config.
func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".ccp-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful Rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// blockRegex returns a compiled pattern that matches the whole alias block
// (including trailing newline) for the given profile name. The pattern is
// anchored on the fenced begin/end comments, which removes jean-claude's
// fragility to mid-block user edits.
func blockRegex(name string) (*regexp.Regexp, error) {
	begin := regexp.QuoteMeta(fmt.Sprintf(aliasBeginFmt, name))
	end := regexp.QuoteMeta(fmt.Sprintf(aliasEndFmt, name))
	return regexp.Compile(`(?ms)^` + begin + `.*?` + end + `\n?`)
}

func readOrEmpty(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}
