package main

import (
	"os"
	"time"
)

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func getenv(key string) string {
	return os.Getenv(key)
}
