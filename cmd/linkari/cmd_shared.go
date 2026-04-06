package main

import "os"

// resolveQueueDB resolves the queue database path from flag, env, or default.
func resolveQueueDB(path string) string {
	if path != "" {
		return path
	}
	if env := os.Getenv("LINKARI_QUEUE_DB"); env != "" {
		return env
	}
	home, _ := os.UserHomeDir()
	return home + "/.config/linkari/queue.db"
}
