package sync

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

// warnWriter is where auth-related warnings go. Tests and JSON-output code
// paths can swap this (via SetAuthWarnWriter) to io.Discard so warnings
// don't land in structured output streams. Defaults to os.Stderr for
// humans running the CLI.
var warnWriter io.Writer = os.Stderr

// SetAuthWarnWriter routes auth warnings (e.g. encrypted-key hint) to w.
// Pass io.Discard to silence them; useful when the caller intends to
// emit JSON on stderr and cannot tolerate interleaved prose.
func SetAuthWarnWriter(w io.Writer) {
	if w == nil {
		w = io.Discard
	}
	warnWriter = w
}

// sshAuthFromEnv builds an SSH auth method from the user's environment.
// Currently: use the SSH agent if SSH_AUTH_SOCK is present, otherwise fall
// back to ~/.ssh/id_ed25519 / id_rsa with an empty passphrase.
//
// Encrypted keys cannot be loaded via the empty-passphrase path — we print
// a warning and return nil so the caller attempts unauthenticated access,
// which will fail loudly against a private remote. Preferable to silently
// returning nil without explaining why the next command fails with
// "authentication required".
//
// HTTPS URLs don't need this — go-git uses no auth for HTTPS by default,
// which works for public repos and for hosts that supply a credential
// helper. If/when we need HTTPS with creds, add token-based auth here.
func sshAuthFromEnv() transport.AuthMethod {
	if os.Getenv("SSH_AUTH_SOCK") != "" {
		auth, err := gitssh.NewSSHAgentAuth("git")
		if err == nil {
			return auth
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	encryptedSeen := false
	for _, name := range []string{"id_ed25519", "id_rsa"} {
		key := home + "/.ssh/" + name
		if _, err := os.Stat(key); err != nil {
			continue
		}
		auth, err := gitssh.NewPublicKeysFromFile("git", key, "")
		if err == nil {
			return auth
		}
		// Common cases: encrypted key (needs passphrase) or malformed key.
		if strings.Contains(err.Error(), "passphrase") || strings.Contains(err.Error(), "encrypted") {
			encryptedSeen = true
		}
	}
	if encryptedSeen {
		fmt.Fprintln(warnWriter, "ccp: ~/.ssh/id_ed25519 or id_rsa appears to be passphrase-encrypted. "+
			"Start ssh-agent and `ssh-add` your key, then re-run.")
	}
	return nil
}
