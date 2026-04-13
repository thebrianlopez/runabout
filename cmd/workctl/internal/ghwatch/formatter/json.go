package formatter

import (
	"encoding/json"
	"io"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/ghwatch/event"
)

// JSON writes one JSON object per line (JSONL) to w.
type JSON struct {
	enc *json.Encoder
}

// NewJSON returns a JSONL formatter writing to w.
func NewJSON(w io.Writer) *JSON {
	return &JSON{enc: json.NewEncoder(w)}
}

// Format encodes a single event as a JSON line.
func (j *JSON) Format(e event.Event) error {
	return j.enc.Encode(e)
}
