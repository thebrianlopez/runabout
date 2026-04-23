package ssh

import (
	"net"
	"os"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// agentAuthMethod returns an SSH auth method backed by the ssh-agent if the
// SSH_AUTH_SOCK environment variable is set. Returns nil if unavailable.
func agentAuthMethod() gossh.AuthMethod {
	sockPath := os.Getenv("SSH_AUTH_SOCK")
	if sockPath == "" {
		return nil
	}
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil
	}
	return gossh.PublicKeysCallback(agent.NewClient(conn).Signers)
}
