package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types/events"
)

func listenCmd() *cobra.Command {
	var (
		from      []string
		clipboard bool
		first     bool
		timeout   time.Duration
		format    string
	)

	cmd := &cobra.Command{
		Use:   "listen",
		Short: "Listen for incoming WhatsApp messages",
		Long: `Connect to WhatsApp and print incoming text messages to stdout.

Use --clipboard to copy each message to the macOS clipboard via pbcopy.
Use --first to exit immediately after receiving the first matching message.

Examples:
  wasend listen
  wasend listen --from 15551234567
  wasend listen --from 15551234567 --clipboard --first
  wasend listen --timeout 5m --clipboard
  wasend listen --format json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, container, err := newClient()
			if err != nil {
				return err
			}
			defer container.Close()

			if client.Store.ID == nil {
				return fmt.Errorf("not logged in — run 'wasend login' first")
			}

			// Normalize sender filter: strip leading + so "15551234567" and "+15551234567" both match.
			filterSet := make(map[string]bool, len(from))
			for _, f := range from {
				filterSet[strings.TrimPrefix(f, "+")] = true
			}

			done := make(chan struct{}, 1)
			connected := make(chan struct{}, 1)

			client.AddEventHandler(func(evt interface{}) {
				switch v := evt.(type) {
				case *events.Connected:
					select {
					case connected <- struct{}{}:
					default:
					}

				case *events.Message:
					if v.Info.IsFromMe {
						return
					}
					if len(filterSet) > 0 && !filterSet[v.Info.Sender.User] {
						return
					}

					text := v.Message.GetConversation()
					if text == "" {
						text = v.Message.GetExtendedTextMessage().GetText()
					}
					if text == "" {
						return // media, sticker, reaction — not a text message
					}

					if format == "json" {
						data, _ := json.Marshal(map[string]string{
							"sender":    v.Info.Sender.User,
							"timestamp": v.Info.Timestamp.Format(time.RFC3339),
							"text":      text,
						})
						fmt.Println(string(data))
					} else {
						fmt.Println(text)
					}

					if clipboard {
						if werr := writeClipboard(text); werr != nil {
							fmt.Fprintf(os.Stderr, "clipboard write error: %v\n", werr)
						}
					}

					if first {
						select {
						case done <- struct{}{}:
						default:
						}
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

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
			defer signal.Stop(sig)

			if timeout > 0 {
				timer := time.NewTimer(timeout)
				defer timer.Stop()
				select {
				case <-done:
				case <-timer.C:
					return fmt.Errorf("timed out waiting for message")
				case <-sig:
					fmt.Fprintln(os.Stderr, "\nInterrupted.")
				}
			} else {
				select {
				case <-done:
				case <-sig:
					fmt.Fprintln(os.Stderr, "\nInterrupted.")
				}
			}

			return nil
		},
	}

	cmd.Flags().StringArrayVarP(&from, "from", "f", nil, "filter by sender phone number, e.g. 15551234567 (repeatable)")
	cmd.Flags().BoolVar(&clipboard, "clipboard", false, "copy each message to clipboard (macOS)")
	cmd.Flags().BoolVar(&first, "first", false, "exit after the first matching message")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "exit if no message arrives within duration (e.g. 30s, 5m)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")

	return cmd
}
