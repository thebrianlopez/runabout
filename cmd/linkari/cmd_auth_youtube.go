package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/thebrianlopez/runabout/internal/secrets"
)

// runYouTubeLoopbackAuthFn is the injectable seam for testing.
var runYouTubeLoopbackAuthFn = runYouTubeLoopbackAuth

// isTerminalFn and pasteReaderFn are injectable seams for the headless paste
// race (EPIC-253). isTerminalFn reports whether stdin is an interactive TTY;
// pasteReaderFn returns the reader used to collect pasted redirect URLs/codes.
var (
	isTerminalFn  = defaultIsTerminal
	pasteReaderFn = defaultPasteReader
)

// youtubeOAuthEndpoint is an injectable seam so tests can point token
// exchange at a fake httptest.Server instead of Google's real endpoint.
var youtubeOAuthEndpoint = google.Endpoint

var youtubeSlotNameRe = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)

// errStateMismatch is returned by parsePastedAuthCode when a URL-form paste
// carries a state value that does not match the state generated for this
// auth attempt.
var errStateMismatch = errors.New("oauth_state_mismatch")

// errPasteUnparseable is returned by parsePastedAuthCode when the pasted
// input matches neither the redirect-URL grammar nor the bare-code grammar.
var errPasteUnparseable = errors.New("oauth_paste_unparseable")

// ctxKeyYouTubeSlot threads the slot name into runYouTubeLoopbackAuth for
// the youtube_auth_code_source log line without changing the function's
// signature (the signature is a test seam contract - see EPIC-253).
type ctxKeyYouTubeSlot struct{}

func defaultIsTerminal(fd int) bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func defaultPasteReader() io.Reader {
	return os.Stdin
}

func authCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate Linkari integrations",
	}
	cmd.AddCommand(authYouTubeCmd())
	return cmd
}

func authYouTubeCmd() *cobra.Command {
	var configFile string
	var queueDB string
	var profile string
	var slotFlag string
	var noBrowser bool
	var callbackAddr string

	cmd := &cobra.Command{
		Use:   "youtube",
		Short: "Re-authorize Google/YouTube access for Watch Later and Liked Videos",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if profile == "" {
				profile = "default"
			}

			// Validate slot name before starting the OAuth flow.
			if !youtubeSlotNameRe.MatchString(slotFlag) {
				return errors.New("slot name must match [a-zA-Z0-9-]+")
			}

			if configFile == "" {
				configFile = os.Getenv("LINKARI_CONFIG")
			}
			cfg, err := LoadConfig(ctx, configFile)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			resolver := secrets.New(secrets.DefaultAWSFactory(secrets.AWSConfig(cfg.Server.AWS)))
			resolveVal := func(env, configVal string) string {
				if v := os.Getenv(env); v != "" {
					return v
				}
				resolved, _, _, rerr := resolveServerField(ctx, resolver, "", "", configVal, "")
				if rerr != nil {
					return ""
				}
				return resolved
			}
			clientID := resolveVal("LINKARI_GOOGLE_CLIENT_ID", cfg.Server.GoogleClientID)
			clientSecret := resolveVal("LINKARI_GOOGLE_CLIENT_SECRET", cfg.Server.GoogleClientSecret)
			if clientID == "" || clientSecret == "" {
				return errors.New("google_client_id and google_client_secret are required in config.toml or LINKARI_GOOGLE_CLIENT_ID/LINKARI_GOOGLE_CLIENT_SECRET")
			}

			queueDB = resolveQueueDB(queueDB)
			q, err := NewQueue(queueDB, false)
			if err != nil {
				return fmt.Errorf("open queue db: %w", err)
			}
			defer q.Close()

			ctx = context.WithValue(ctx, ctxKeyYouTubeSlot{}, slotFlag)
			tok, err := runYouTubeLoopbackAuthFn(ctx, clientID, clientSecret, callbackAddr, noBrowser)
			if err != nil {
				return err
			}
			if tok.RefreshToken == "" {
				return errors.New("google did not return a refresh token; retry with consent or revoke prior app access, then run linkari auth youtube again")
			}
			expiresAt := tok.Expiry.Unix()
			if expiresAt <= 0 {
				expiresAt = time.Now().Add(time.Hour).Unix()
			}

			// Write to the slots table (primary path).
			if err := q.SetYouTubeSlotToken(1, slotFlag, tok.RefreshToken, expiresAt); err != nil {
				return fmt.Errorf("store youtube slot token: %w", err)
			}
			// Backward compat: also write to the legacy users column for the default slot
			// during the soak window, so existing youtubeTokenSource callers still work.
			if slotFlag == "default" {
				if err := storeYouTubeToken(q, profile, tok.RefreshToken, expiresAt); err != nil {
					return fmt.Errorf("store youtube token: %w", err)
				}
			}

			fmt.Fprintf(cmd.OutOrStdout(), "YouTube credential saved to slot %q.\n", slotFlag)
			return nil
		},
	}
	cmd.Flags().StringVar(&configFile, "config", "", "path to config.toml (default ~/.config/linkari/config.toml, or LINKARI_CONFIG)")
	cmd.Flags().StringVar(&queueDB, "queue-db", "", "path to SQLite queue database (or LINKARI_QUEUE_DB)")
	cmd.Flags().StringVar(&profile, "profile", "default", "Linkari profile/user token slot to update")
	cmd.Flags().StringVar(&slotFlag, "slot", "default", "OAuth credential slot name (default: \"default\")")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print auth URL instead of opening browser")
	cmd.Flags().StringVar(&callbackAddr, "callback-addr", "127.0.0.1:53682", "OAuth loopback callback address; register http://127.0.0.1:53682/callback in Google Cloud")
	return cmd
}

// pasteEvent is emitted by the stdin paste acceptor loop: either a
// successfully parsed code, or a parse error along with the running count of
// bad attempts (used to enforce the 3-strikes fatal bound).
type pasteEvent struct {
	code string
	err  error
	bad  int
}

// pasteAcceptLoop reads lines from r, attempting to parse each as a pasted
// OAuth redirect URL or bare code. It emits one event per line until either a
// valid code is found (loop exits) or 3 unparseable/mismatched attempts have
// been emitted (loop exits, accepted leak if r is still blocked on read - see
// EPIC-253 TDD Reader Lifetime decision).
func pasteAcceptLoop(r io.Reader, wantState string, out chan<- pasteEvent) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	bad := 0
	for scanner.Scan() {
		code, err := parsePastedAuthCode(scanner.Text(), wantState)
		if err != nil {
			bad++
			out <- pasteEvent{err: err, bad: bad}
			if bad >= 3 {
				return
			}
			continue
		}
		out <- pasteEvent{code: code}
		return
	}
}

// parsePastedAuthCode returns the authorization code from pasted input.
// URL form (contains "://"): state must match wantState exactly
// (errStateMismatch otherwise) and a non-empty `code` query param must be
// present (errPasteUnparseable otherwise).
// Bare form: input must be a single whitespace-free token of at least 16
// characters and must not contain "://"; no state is required (FDD Q1=A).
func parsePastedAuthCode(input, wantState string) (string, error) {
	trimmed := strings.TrimSpace(input)
	trimmed = strings.Trim(trimmed, `"'`)
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return "", errPasteUnparseable
	}

	if strings.Contains(trimmed, "://") {
		u, err := url.Parse(trimmed)
		if err != nil {
			return "", errPasteUnparseable
		}
		code := u.Query().Get("code")
		if code == "" {
			return "", errPasteUnparseable
		}
		if u.Query().Get("state") != wantState {
			return "", errStateMismatch
		}
		return code, nil
	}

	// Bare code form.
	if strings.ContainsAny(trimmed, " \t\r\n") {
		return "", errPasteUnparseable
	}
	if len(trimmed) < 16 {
		return "", errPasteUnparseable
	}
	return trimmed, nil
}

func runYouTubeLoopbackAuth(ctx context.Context, clientID, clientSecret, callbackAddr string, noBrowser bool) (*oauth2.Token, error) {
	if callbackAddr == "" {
		callbackAddr = "127.0.0.1:53682"
	}

	slot, _ := ctx.Value(ctxKeyYouTubeSlot{}).(string)
	if slot == "" {
		slot = "unknown"
	}

	tty := isTerminalFn(int(os.Stdin.Fd()))

	ln, listenErr := net.Listen("tcp", callbackAddr)
	pasteOnly := false
	if listenErr != nil {
		if tty && errors.Is(listenErr, syscall.EADDRINUSE) {
			fmt.Fprintf(os.Stderr, "loopback unavailable (port in use) - paste-only mode\n")
			pasteOnly = true
		} else {
			return nil, fmt.Errorf("listen loopback: %w", listenErr)
		}
	}

	state, err := randomURLSafe(24)
	if err != nil {
		return nil, err
	}

	var redirectURL string
	if pasteOnly {
		redirectURL = "http://" + callbackAddr + "/callback"
	} else {
		redirectURL = "http://" + ln.Addr().String() + "/callback"
		defer ln.Close()
	}

	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes: []string{
			"https://www.googleapis.com/auth/youtube.readonly",
			"https://www.googleapis.com/auth/youtube",
		},
		Endpoint:    youtubeOAuthEndpoint,
		RedirectURL: redirectURL,
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	if !pasteOnly {
		mux := http.NewServeMux()
		mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("state"); got != state {
				http.Error(w, "invalid state", http.StatusBadRequest)
				errCh <- errors.New("oauth callback state mismatch")
				return
			}
			if e := r.URL.Query().Get("error"); e != "" {
				http.Error(w, e, http.StatusBadRequest)
				errCh <- fmt.Errorf("oauth error: %s", e)
				return
			}
			code := r.URL.Query().Get("code")
			if code == "" {
				http.Error(w, "missing code", http.StatusBadRequest)
				errCh <- errors.New("oauth callback missing code")
				return
			}
			fmt.Fprintln(w, "Linkari YouTube authentication complete. You may close this tab.")
			codeCh <- code
		})

		srv := &http.Server{Handler: mux}
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
		defer srv.Shutdown(context.Background())
	}

	authURL := cfg.AuthCodeURL(
		state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)
	fmt.Fprintf(os.Stderr, "Open this URL to authorize YouTube access:\n%s\n", authURL)

	pasteCh := make(chan pasteEvent, 4)
	if tty {
		fmt.Fprintf(os.Stderr, "\nNo browser on this machine? After approving, the redirect page will fail to load -\nthat is expected. Paste the full redirect URL (or just the code) here:\n")
		go pasteAcceptLoop(pasteReaderFn(), state, pasteCh)
	}

	if !noBrowser && !pasteOnly {
		_ = openBrowser(authURL)
	}

	exchange := func(code, source string) (*oauth2.Token, error) {
		tok, err := cfg.Exchange(ctx, code)
		if err != nil {
			return nil, fmt.Errorf("exchange oauth code: %w", err)
		}
		fmt.Fprintf(os.Stderr, "youtube_auth_code_source source=%s slot=%s\n", source, slot)
		return tok, nil
	}

	timeoutCh := time.After(5 * time.Minute)
	for {
		select {
		case code := <-codeCh:
			return exchange(code, "loopback")
		case ev := <-pasteCh:
			if ev.err != nil {
				if errors.Is(ev.err, errStateMismatch) {
					fmt.Fprintln(os.Stderr, "state mismatch - paste the redirect from THIS login attempt")
				} else {
					fmt.Fprintln(os.Stderr, "could not find an authorization code - paste the full redirect URL")
				}
				if ev.bad >= 3 {
					return nil, errPasteUnparseable
				}
				continue
			}
			return exchange(ev.code, "paste")
		case err := <-errCh:
			return nil, err
		case <-timeoutCh:
			return nil, errors.New("timed out waiting for oauth callback")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate oauth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
