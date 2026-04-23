package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAliasBlockFormat(t *testing.T) {
	got := AliasBlock("work")
	for _, want := range []string{
		"# >>> ccp profile: work >>>",
		`alias claude-work='CLAUDE_CONFIG_DIR="$HOME/.claude-work" claude'`,
		"# <<< ccp profile: work <<<",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestInstallAppendsBlockPreservingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".zshrc")
	if err := os.WriteFile(path, []byte("# user content\nalias ll='ls -l'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InstallAlias(path, "work"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	content := string(b)
	if !strings.HasPrefix(content, "# user content") {
		t.Errorf("existing content damaged: %q", content)
	}
	if !strings.Contains(content, "alias claude-work=") {
		t.Errorf("alias missing:\n%s", content)
	}
}

func TestInstallReplacesPriorBlockForSameProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".zshrc")
	// Pre-seed with a corrupted block (2 alias lines).
	old := "# >>> ccp profile: work >>>\nalias claude-work='OLD'\nalias claude-extra='oops'\n# <<< ccp profile: work <<<\n"
	if err := os.WriteFile(path, []byte("user\n"+old), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InstallAlias(path, "work"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	content := string(b)
	if strings.Contains(content, "OLD") {
		t.Errorf("old block not replaced:\n%s", content)
	}
	if strings.Contains(content, "claude-extra") {
		t.Errorf("orphan alias line not removed (jean-claude's bug):\n%s", content)
	}
}

func TestUninstallRemovesOnlyMatchingBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".zshrc")
	if err := InstallAlias(path, "work"); err != nil {
		t.Fatal(err)
	}
	if err := InstallAlias(path, "personal"); err != nil {
		t.Fatal(err)
	}
	if err := UninstallAlias(path, "work"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	content := string(b)
	if strings.Contains(content, "claude-work") {
		t.Errorf("work block still present:\n%s", content)
	}
	if !strings.Contains(content, "claude-personal") {
		t.Errorf("personal block accidentally removed:\n%s", content)
	}
}

func TestUninstallMissingFileIsOk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent-rc")
	if err := UninstallAlias(path, "work"); err != nil {
		t.Errorf("UninstallAlias on missing file: %v", err)
	}
}
