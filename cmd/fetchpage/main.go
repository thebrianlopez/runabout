package main

import (
	"fmt"
	"os"

	"github.com/playwright-community/playwright-go"
	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	var (
		timeout float64
		wait    float64
		channel string
	)

	rootCmd := &cobra.Command{
		Use:   "fetchpage <url>",
		Short: "Fetch fully rendered HTML from a URL using headless Chrome",
		Long:  "Launches headless Chromium via Playwright, waits for JS rendering, and outputs the fully rendered HTML to stdout.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fetchPage(args[0], timeout, wait, channel)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.Flags().Float64Var(&timeout, "timeout", 30, "navigation timeout in seconds")
	rootCmd.Flags().Float64Var(&wait, "wait", 3, "seconds to wait after page load")
	rootCmd.Flags().StringVar(&channel, "channel", "chrome", "browser channel (chrome, chromium)")

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(os.Stderr, "fetchpage %s (%s) built %s\n", version, commit, date)
		},
	})

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "fetchpage: %v\n", err)
		os.Exit(1)
	}
}

func fetchPage(url string, timeoutSec, waitSec float64, channel string) error {
	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("could not start playwright: %w\nRun: go run github.com/playwright-community/playwright-go/cmd/playwright install --with-deps", err)
	}
	defer pw.Stop()

	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
		Channel:  playwright.String(channel),
	})
	if err != nil {
		return fmt.Errorf("could not launch browser (channel=%s): %w", channel, err)
	}
	defer browser.Close()

	page, err := browser.NewPage()
	if err != nil {
		return fmt.Errorf("could not create page: %w", err)
	}

	timeoutMs := timeoutSec * 1000
	_, err = page.Goto(url, playwright.PageGotoOptions{
		Timeout:   playwright.Float(timeoutMs),
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	})
	if err != nil {
		return fmt.Errorf("navigation failed: %w", err)
	}

	// Extra wait for JS-heavy pages / Cloudflare challenges
	page.WaitForTimeout(waitSec * 1000)

	content, err := page.Content()
	if err != nil {
		return fmt.Errorf("could not get page content: %w", err)
	}

	fmt.Print(content)
	return nil
}
