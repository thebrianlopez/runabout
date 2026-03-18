package main

import (
	"fmt"
	"io"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

// resolveMessage reads the message body from either stdin or command args.
func resolveMessage(stdin bool, args []string, reader io.Reader) (string, error) {
	if stdin {
		data, err := io.ReadAll(reader)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	if len(args) > 0 {
		return strings.Join(args, " "), nil
	}
	return "", fmt.Errorf("provide a message as arguments or use --stdin")
}

// parseRecipient converts a phone number string into a WhatsApp JID.
func parseRecipient(phone string) (types.JID, error) {
	jid, err := types.ParseJID(phone + "@s.whatsapp.net")
	if err != nil {
		return types.JID{}, fmt.Errorf("invalid phone number %q: %w", phone, err)
	}
	return jid, nil
}
