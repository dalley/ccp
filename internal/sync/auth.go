package sync

import (
	"os"

	"github.com/go-git/go-git/v5/plumbing/transport"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

// sshAuthFromEnv builds an SSH auth method from the user's environment.
// Currently: use the SSH agent if SSH_AUTH_SOCK is present, otherwise fall
// back to ~/.ssh/id_ed25519 / id_rsa with a default password of "".
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
	for _, name := range []string{"id_ed25519", "id_rsa"} {
		key := home + "/.ssh/" + name
		if _, err := os.Stat(key); err != nil {
			continue
		}
		auth, err := gitssh.NewPublicKeysFromFile("git", key, "")
		if err == nil {
			return auth
		}
	}
	return nil
}
