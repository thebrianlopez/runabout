package config

import (
	"os"
	"path/filepath"
)

const configScaffold = `# bmux configuration
# Reference: https://github.com/blo-grindr/bmux

hosts:
  - name: my-server          # unique name; used as local tmux session name
    ssh_host: example.com    # required: SSH hostname or IP
    ssh_user: ubuntu         # required: SSH username
    ssh_port: 22             # optional: default 22
    # identity_file: ~/.ssh/id_ed25519  # optional: falls back to ssh-agent

reconnect:
  initial_interval: 2s   # delay before first reconnect attempt
  max_interval: 5m       # maximum delay (exponential backoff cap)
  multiplier: 2.0        # backoff multiplier

log:
  format: text           # text | json
  level: info            # debug | info | warn | error
`

// InitConfig scaffolds a commented config.yaml at path.
// Returns config_file_exists if the file already exists and force is false.
// Creates parent directories as needed.
func InitConfig(path string, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return errFileExists(path)
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	return os.WriteFile(path, []byte(configScaffold), 0o600)
}
