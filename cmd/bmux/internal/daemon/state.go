package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteStatus writes status to path atomically using a rename.
// Writes to <path>.tmp then renames to path — readers always see a complete file.
func WriteStatus(path string, status *DaemonStatus) error {
	tmp := path + ".tmp"
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("marshal status: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdirall for status: %w", err)
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write status tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename status: %w", err)
	}
	return nil
}

// ReadStatus reads and unmarshals status.json from path.
func ReadStatus(path string) (*DaemonStatus, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errStateUnreadable("file not found")
		}
		return nil, errStateUnreadable(err.Error())
	}
	var s DaemonStatus
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, errStateUnreadable(fmt.Sprintf("json unmarshal: %s", err))
	}
	return &s, nil
}
