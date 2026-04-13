package formatter

import "github.com/blo-grindr/runabout/cmd/workctl/internal/ghwatch/event"

// Formatter writes events to the output stream.
type Formatter interface {
	Format(event.Event) error
}
