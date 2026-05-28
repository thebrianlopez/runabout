package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	_ "github.com/mattn/go-sqlite3"
)

// newClient creates a whatsmeow client with the configured database path.
func newClient() (*whatsmeow.Client, *sqlstore.Container, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, nil, fmt.Errorf("create db directory: %w", err)
	}

	var logger waLog.Logger
	if debug {
		logger = waLog.Stdout("wasend", "DEBUG", true)
	} else {
		logger = waLog.Noop
	}

	container, err := sqlstore.New(context.Background(), "sqlite3", "file:"+dbPath+"?_foreign_keys=on", logger)
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		container.Close()
		return nil, nil, fmt.Errorf("get device: %w", err)
	}

	return whatsmeow.NewClient(deviceStore, logger), container, nil
}
