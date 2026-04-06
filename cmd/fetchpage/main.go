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
		stealth bool
	)

	rootCmd := &cobra.Command{
		Use:   "fetchpage <url>",
		Short: "Fetch fully rendered HTML from a URL using headless Chrome",
		Long:  "Launches headless Chromium via Playwright, waits for JS rendering, and outputs the fully rendered HTML to stdout.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fetchPage(args[0], timeout, wait, channel, stealth)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.Flags().Float64Var(&timeout, "timeout", 30, "navigation timeout in seconds")
	rootCmd.Flags().Float64Var(&wait, "wait", 3, "seconds to wait after page load")
	rootCmd.Flags().StringVar(&channel, "channel", "chrome", "browser channel (chrome, chromium)")
	rootCmd.Flags().BoolVar(&stealth, "stealth", false, "enable anti-detection: realistic fingerprint + JS overrides")

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

// stealthJS overrides common bot-detection signals before page scripts run.
const stealthJS = `
Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
Object.defineProperty(navigator, 'languages', { get: () => ['en-US', 'en'] });
Object.defineProperty(navigator, 'plugins', {
  get: () => [1, 2, 3, 4, 5],
});
window.chrome = { runtime: {} };
const originalQuery = window.navigator.permissions.query;
window.navigator.permissions.query = (parameters) =>
  parameters.name === 'notifications'
    ? Promise.resolve({ state: Notification.permission })
    : originalQuery(parameters);
`

func fetchPage(url string, timeoutSec, waitSec float64, channel string, stealth bool) error {
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

	var page playwright.Page

	if stealth {
		ctx, err := browser.NewContext(playwright.BrowserNewContextOptions{
			UserAgent:      playwright.String("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"),
			Viewport:       &playwright.Size{Width: 1920, Height: 1080},
			Locale:         playwright.String("en-US"),
			TimezoneId:     playwright.String("America/New_York"),
			DeviceScaleFactor: playwright.Float(1),
		})
		if err != nil {
			return fmt.Errorf("could not create browser context: %w", err)
		}
		defer ctx.Close()

		page, err = ctx.NewPage()
		if err != nil {
			return fmt.Errorf("could not create page: %w", err)
		}

		if err := page.AddInitScript(playwright.Script{Content: playwright.String(stealthJS)}); err != nil {
			return fmt.Errorf("could not inject stealth script: %w", err)
		}
	} else {
		var err error
		page, err = browser.NewPage()
		if err != nil {
			return fmt.Errorf("could not create page: %w", err)
		}
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
