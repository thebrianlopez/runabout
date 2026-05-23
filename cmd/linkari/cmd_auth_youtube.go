package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

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
	var noBrowser bool

	cmd := &cobra.Command{
		Use:   "youtube",
		Short: "Re-authorize Google/YouTube access for Watch Later and Liked Videos",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if profile == "" {
				profile = "default"
			}

			if configFile == "" {
				configFile = os.Getenv("LINKARI_CONFIG")
			}
			cfg, err := LoadConfig(ctx, configFile)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			clientID := firstNonEmpty(os.Getenv("LINKARI_GOOGLE_CLIENT_ID"), cfg.Server.GoogleClientID)
			clientSecret := firstNonEmpty(os.Getenv("LINKARI_GOOGLE_CLIENT_SECRET"), cfg.Server.GoogleClientSecret)
			if clientID == "" || clientSecret == "" {
				return errors.New("google_client_id and google_client_secret are required in config.toml or LINKARI_GOOGLE_CLIENT_ID/LINKARI_GOOGLE_CLIENT_SECRET")
			}

			queueDB = resolveQueueDB(queueDB)
			q, err := NewQueue(queueDB, false)
			if err != nil {
				return fmt.Errorf("open queue db: %w", err)
			}
			defer q.Close()

			tok, err := runYouTubeLoopbackAuth(ctx, clientID, clientSecret, noBrowser)
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
			if err := storeYouTubeToken(q, profile, tok.RefreshToken, expiresAt); err != nil {
				return fmt.Errorf("store youtube token: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "YouTube reauth complete for profile %q. Stored refresh token in %s.\n", profile, queueDB)
			fmt.Fprintln(cmd.OutOrStdout(), "Validate with: linkari doctor && POST /sync/youtube-watchlater")
			return nil
		},
	}
	cmd.Flags().StringVar(&configFile, "config", "", "path to config.toml (default ~/.config/linkari/config.toml, or LINKARI_CONFIG)")
	cmd.Flags().StringVar(&queueDB, "queue-db", "", "path to SQLite queue database (or LINKARI_QUEUE_DB)")
	cmd.Flags().StringVar(&profile, "profile", "default", "Linkari profile/user token slot to update")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print auth URL instead of opening browser")
	return cmd
}

func runYouTubeLoopbackAuth(ctx context.Context, clientID, clientSecret string, noBrowser bool) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen loopback: %w", err)
	}
	defer ln.Close()

	state, err := randomURLSafe(24)
	if err != nil {
		return nil, err
	}
	redirectURL := "http://" + ln.Addr().String() + "/callback"
	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes: []string{
			"https://www.googleapis.com/auth/youtube.readonly",
			"https://www.googleapis.com/auth/youtube",
		},
		Endpoint:    google.Endpoint,
		RedirectURL: redirectURL,
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
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

	authURL := cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)
	fmt.Fprintf(os.Stderr, "Open this URL to authorize YouTube access:\n%s\n", authURL)
	if !noBrowser {
		_ = openBrowser(authURL)
	}

	select {
	case code := <-codeCh:
		tok, err := cfg.Exchange(ctx, code)
		if err != nil {
			return nil, fmt.Errorf("exchange oauth code: %w", err)
		}
		return tok, nil
	case err := <-errCh:
		return nil, err
	case <-time.After(5 * time.Minute):
		return nil, errors.New("timed out waiting for oauth callback")
	case <-ctx.Done():
		return nil, ctx.Err()
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
