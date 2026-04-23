package cli

import (
	"strings"
	"testing"
)

func TestPromptEmptyWhenNoActiveProfile(t *testing.T) {
	setupCLI(t)
	out, _, err := runCLI(t, "", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("expected empty, got %q", out)
	}
}

func TestPromptPrintsActiveProfileWithAffixes(t *testing.T) {
	setupCLI(t)
	if _, _, err := runCLI(t, "", "profile", "create", "work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "use", "work"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "prompt", "--prefix", "[", "--suffix", "] ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[work]" {
		t.Errorf("prompt = %q, want '[work] '", out)
	}
}

func TestCompletionProducesScriptForEachShell(t *testing.T) {
	setupCLI(t)
	for _, shell := range []string{"zsh", "bash", "fish"} {
		out, _, err := runCLI(t, "", "completion", shell)
		if err != nil {
			t.Errorf("completion %s: %v", shell, err)
			continue
		}
		if len(out) < 100 {
			t.Errorf("completion %s suspiciously short (%d bytes)", shell, len(out))
		}
	}
}
