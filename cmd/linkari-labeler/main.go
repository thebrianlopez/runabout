package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: linkari-labeler <keygen|serve> [options]\n")
		os.Exit(1)
	}

	cfgPath := os.ExpandEnv("$HOME/.config/linkari/labeler.yaml")

	switch os.Args[1] {
	case "keygen":
		runKeygen(cfgPath)
	case "serve":
		cfg, err := loadLabelerConfig(cfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load config: %v\n", err)
			os.Exit(1)
		}
		if err := validateLabelerConfig(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", err.Error())
			os.Exit(1)
		}
		db, err := OpenReadOnlyDB(os.ExpandEnv(cfg.QueueDBPath))
		if err != nil {
			fmt.Fprintf(os.Stderr, "open db: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()

		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		srv := newLabelerServer(cfg, db)
		if err := srv.run(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "server error: %v\n", err)
			os.Exit(1)
		}
	case "--help", "help":
		fmt.Println("linkari-labeler keygen  -- generate HMAC-SHA256 signing key")
		fmt.Println("linkari-labeler serve   -- start labeler XRPC server")
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		os.Exit(1)
	}
}
