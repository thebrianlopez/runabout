package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

var (
	version = "0.1.0"
	commit  = "dev"
	date    = "unknown"
)

var (
	dbPath string
	debug  bool
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "wasend",
		Short:   "Send WhatsApp messages from the command line",
		Version: fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date),
	}

	home, _ := os.UserHomeDir()
	defaultDB := filepath.Join(home, ".wasend", "session.db")
	rootCmd.PersistentFlags().StringVar(&dbPath, "db", defaultDB, "path to session database")
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "enable debug logging")

	rootCmd.AddCommand(loginCmd())
	rootCmd.AddCommand(sendCmd())
	rootCmd.AddCommand(listenCmd())
	rootCmd.AddCommand(logoutCmd())

	t := instrument(rootCmd, "wasend")
	err := rootCmd.Execute()
	t.emit(err)
	if err != nil {
		os.Exit(1)
	}
}

func loginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Authenticate with WhatsApp via QR code",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, container, err := newClient()
			if err != nil {
				return err
			}
			defer container.Close()

			if client.Store.ID != nil {
				fmt.Println("Already logged in as", client.Store.ID)
				return nil
			}

			client.AddEventHandler(func(evt interface{}) {
				if debug {
					fmt.Fprintf(os.Stderr, "[event] %T: %+v\n", evt, evt)
				}
			})

			qrChan, err := client.GetQRChannel(context.Background())
			if err != nil {
				return fmt.Errorf("get QR channel: %w", err)
			}
			if debug {
				fmt.Fprintln(os.Stderr, "[debug] QR channel obtained, connecting...")
			}

			if err := client.Connect(); err != nil {
				return fmt.Errorf("connect: %w", err)
			}
			defer client.Disconnect()
			if debug {
				fmt.Fprintln(os.Stderr, "[debug] connected, waiting for QR events...")
			}

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

			fmt.Println("Scan this QR code with WhatsApp on your phone:")
			fmt.Println()

			for {
				select {
				case evt, ok := <-qrChan:
					if !ok {
						return fmt.Errorf("QR channel closed unexpectedly")
					}
					if debug {
						fmt.Fprintf(os.Stderr, "[debug] QR event: %s\n", evt.Event)
					}
					switch evt.Event {
					case "code":
						qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
						fmt.Println("\nWaiting for scan...")
					case "success":
						fmt.Println("Login successful!")
						// Give the client time to persist the session before defer disconnect
						time.Sleep(2 * time.Second)
						return nil
					case "timeout":
						return fmt.Errorf("QR code timed out — run login again")
					}
				case <-sig:
					fmt.Println("\nInterrupted.")
					return nil
				}
			}
		},
	}
}

func sendCmd() *cobra.Command {
	var (
		to        string
		stdin     bool
		clipboard bool
	)

	cmd := &cobra.Command{
		Use:   `send -t PHONE "message"`,
		Short: "Send a WhatsApp message",
		Long: `Send a text message to a WhatsApp number.

The recipient is a phone number in international format (digits only, no +).
The message can be passed as arguments, piped via stdin with --stdin, or read
from the macOS clipboard with --clipboard.

Examples:
  wasend send -t 15551234567 "Hello from CLI"
  echo "Hello" | wasend send -t 15551234567 --stdin
  wasend send -t 15551234567 --stdin < message.txt
  wasend send -t 15551234567 --clipboard`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var (
				text string
				err  error
			)
			if clipboard {
				text, err = readClipboard()
				if err != nil {
					return err
				}
			} else {
				text, err = resolveMessage(stdin, args, os.Stdin)
				if err != nil {
					return err
				}
			}
			if text == "" {
				return fmt.Errorf("message cannot be empty")
			}

			jid, err := parseRecipient(to)
			if err != nil {
				return err
			}

			client, container, err := newClient()
			if err != nil {
				return err
			}
			defer container.Close()

			if client.Store.ID == nil {
				return fmt.Errorf("not logged in — run 'wasend login' first")
			}

			connected := make(chan struct{}, 1)
			client.AddEventHandler(func(evt interface{}) {
				switch evt.(type) {
				case *events.Connected:
					select {
					case connected <- struct{}{}:
					default:
					}
				}
			})

			if err := client.Connect(); err != nil {
				return fmt.Errorf("connect: %w", err)
			}
			defer client.Disconnect()

			select {
			case <-connected:
			case <-time.After(15 * time.Second):
				return fmt.Errorf("timed out waiting for WhatsApp connection")
			}

			resp, err := client.SendMessage(context.Background(), jid, &waProto.Message{
				Conversation: proto.String(text),
			})
			if err != nil {
				return fmt.Errorf("send: %w", err)
			}

			fmt.Printf("Sent (ID: %s, Timestamp: %s)\n", resp.ID, resp.Timestamp.Format("15:04:05"))
			return nil
		},
	}

	cmd.Flags().StringVarP(&to, "to", "t", "", "recipient phone number (digits only, e.g. 15551234567)")
	cmd.Flags().BoolVar(&stdin, "stdin", false, "read message from stdin")
	cmd.Flags().BoolVar(&clipboard, "clipboard", false, "read message from clipboard (macOS)")
	cmd.MarkFlagRequired("to")

	return cmd
}

func logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove stored WhatsApp session",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, container, err := newClient()
			if err != nil {
				return err
			}
			defer container.Close()

			if client.Store.ID == nil {
				fmt.Println("No active session.")
				return nil
			}

			if err := client.Store.Delete(context.Background()); err != nil {
				return fmt.Errorf("delete session: %w", err)
			}

			if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove database: %w", err)
			}

			fmt.Println("Logged out and session removed.")
			return nil
		},
	}
}
