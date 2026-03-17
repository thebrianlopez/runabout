package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	proton "github.com/ProtonMail/go-proton-api"
)

type exportConfig struct {
	username    string
	password    string
	senderEmail string
	outputDir   string
	workers     int
}

func runExport(cfg exportConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Step 1: Authenticate
	fmt.Println("Authenticating...")
	jar, _ := cookiejar.New(&cookiejar.Options{})
	m := proton.New(
		proton.WithAppVersion("macos-mail-export@1.0.0"),
		proton.WithCookieJar(jar),
	)

	c, auth, err := m.NewClientWithLogin(ctx, cfg.username, []byte(cfg.password))
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}
	defer c.Close()

	// Step 2: Handle 2FA if needed
	if auth.TwoFA.Enabled&proton.HasTOTP != 0 {
		fmt.Print("Enter TOTP code: ")
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		totp := strings.TrimSpace(scanner.Text())
		if err := c.Auth2FA(ctx, proton.Auth2FAReq{TwoFactorCode: totp}); err != nil {
			return fmt.Errorf("2FA failed: %w", err)
		}
	}
	fmt.Println("Authenticated.")

	// Step 3: Unlock keys
	fmt.Println("Unlocking keys...")
	user, err := c.GetUser(ctx)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}

	addrs, err := c.GetAddresses(ctx)
	if err != nil {
		return fmt.Errorf("get addresses: %w", err)
	}

	salts, err := c.GetSalts(ctx)
	if err != nil {
		return fmt.Errorf("get salts: %w", err)
	}

	saltedKeyPass, err := salts.SaltForKey([]byte(cfg.password), user.Keys.Primary().ID)
	if err != nil {
		return fmt.Errorf("salt key: %w", err)
	}

	_, addrKRs, err := proton.Unlock(user, addrs, saltedKeyPass, nil)
	if err != nil {
		return fmt.Errorf("unlock keys: %w", err)
	}
	fmt.Printf("Unlocked %d address key(s).\n", len(addrKRs))

	// Step 4: Fetch message metadata
	fmt.Println("Fetching message metadata...")
	metadata, err := c.GetMessageMetadata(ctx, proton.MessageFilter{})
	if err != nil {
		return fmt.Errorf("get metadata: %w", err)
	}
	fmt.Printf("Found %d total messages.\n", len(metadata))

	// Step 5: Filter by contact
	contactFilter := strings.ToLower(cfg.senderEmail)
	var matched []proton.MessageMetadata
	for _, meta := range metadata {
		if matchesContact(meta, contactFilter) {
			matched = append(matched, meta)
		}
	}
	fmt.Printf("Found %d messages with %s.\n", len(matched), cfg.senderEmail)

	if len(matched) == 0 {
		fmt.Println("Nothing to export.")
		return nil
	}

	// Step 6: Create output directory
	if err := os.MkdirAll(cfg.outputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	// Step 7: Filter out already-exported messages
	var toExport []proton.MessageMetadata
	skipped := 0
	for _, meta := range matched {
		outPath := filepath.Join(cfg.outputDir, buildFilename(meta))
		if _, err := os.Stat(outPath); err == nil {
			skipped++
			continue
		}
		toExport = append(toExport, meta)
	}
	fmt.Printf("To export: %d (skipping %d already exported).\n", len(toExport), skipped)

	if len(toExport) == 0 {
		fmt.Println("Nothing new to export.")
		return nil
	}

	// Step 8: Export concurrently with worker pool
	var exported, errCount atomic.Int64
	var wg sync.WaitGroup
	work := make(chan int, cfg.workers)

	for w := 0; w < cfg.workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range work {
				meta := toExport[idx]
				outPath := filepath.Join(cfg.outputDir, buildFilename(meta))

				msg, err := c.GetMessage(ctx, meta.ID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  error fetching %s: %v\n", meta.ID, err)
					errCount.Add(1)
					continue
				}

				kr, ok := addrKRs[msg.AddressID]
				if !ok {
					fmt.Fprintf(os.Stderr, "  no keyring for address %s\n", msg.AddressID)
					errCount.Add(1)
					continue
				}

				decrypted, err := msg.Decrypt(kr)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  decrypt failed %s: %v\n", meta.ID, err)
					errCount.Add(1)
					continue
				}

				body := extractBody(decrypted, msg.MIMEType)
				md := buildMarkdown(meta, body)

				if err := os.WriteFile(outPath, []byte(md), 0o644); err != nil {
					fmt.Fprintf(os.Stderr, "  write failed: %v\n", err)
					errCount.Add(1)
					continue
				}

				n := exported.Add(1)
				if n%50 == 0 {
					fmt.Printf("  exported %d/%d...\n", n, len(toExport))
				}
			}
		}()
	}

	for i := range toExport {
		work <- i
	}
	close(work)
	wg.Wait()

	fmt.Printf("\nDone. Exported: %d, Skipped: %d, Errors: %d\n", exported.Load(), skipped, errCount.Load())
	return nil
}
