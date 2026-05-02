package mirror

import (
	"encoding/json"
	"fmt"
)

// ipcRequest is a JSON line sent from Go to the Node subprocess.
type ipcRequest struct {
	ID   string `json:"id"`
	Op   string `json:"op"`
	Pane string `json:"pane"`
	Data string `json:"data,omitempty"` // base64-encoded bytes for write op
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// ipcResponse is a JSON line received from the Node subprocess.
type ipcResponse struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Data  string `json:"data,omitempty"` // base64-encoded ANSI bytes for snapshot op
	Error string `json:"error,omitempty"`
}

func marshalRequest(req ipcRequest) ([]byte, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal ipc request: %w", err)
	}
	return append(b, '\n'), nil
}

func unmarshalResponse(line []byte) (ipcResponse, error) {
	var resp ipcResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return ipcResponse{}, &MirrorError{
			Code:    ErrCodeIPCParseError,
			Message: fmt.Sprintf("ipc parse error: %v", err),
		}
	}
	return resp, nil
}
