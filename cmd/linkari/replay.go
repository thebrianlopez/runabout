package main

import (
	"log"
	"time"
)

// StartReplay runs a background goroutine that replays pending queue items
// through the router when the tmux session becomes available.
func StartReplay(q *Queue, router *Router, tmux *TmuxRunner, interval time.Duration, debug bool) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			if !tmux.serverRunning() {
				if debug {
					log.Printf("[DEBUG] replay: tmux server not running, skipping")
				}
				continue
			}

			items, err := q.Pending()
			if err != nil {
				log.Printf("WARN: replay: pending query failed: %v", err)
				continue
			}
			if len(items) == 0 {
				continue
			}

			log.Printf("replay: %d pending items, tmux session available", len(items))
			for _, it := range items {
				req := &ShareRequest{
					Type:    it.Type,
					Action:  it.Action,
					Text:    it.Text,
					URL:     it.URL,
					Profile: it.Profile,
					Enter:   true,
				}
				result, err := router.Route(req)
				if err != nil {
					log.Printf("replay: id=%d failed: %v", it.ID, err)
					q.MarkFailed(it.ID)
					continue
				}
				q.MarkRelayed(it.ID)
				log.Printf("replay: id=%d type=%s → %s", it.ID, it.Type, result)
			}
		}
	}()
}
