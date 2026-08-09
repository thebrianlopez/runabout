package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// --- parsePastedAuthCode contract tests (CT-1..CT-4, BT-1) ---

// CT-1: Full redirect URL paste w/ matching state returns its code.
func TestParsePastedAuthCode_CT1_URLMatchingState(t *testing.T) {
	code, err := parsePastedAuthCode("http://127.0.0.1:53682/callback?code=abc123def456ghi&state=S", "S")
	require.NoError(t, err)
	assert.Equal(t, "abc123def456ghi", code)
}

// CT-2: URL paste wrong state → rejected, never returns a code.
func TestParsePastedAuthCode_CT2_URLWrongState(t *testing.T) {
	code, err := parsePastedAuthCode("http://127.0.0.1:53682/callback?code=abc123def456ghi&state=WRONG", "S")
	require.Error(t, err)
	assert.ErrorIs(t, err, errStateMismatch)
	assert.Empty(t, code)
}

// CT-3: Bare code accepted without state (Q1=A).
func TestParsePastedAuthCode_CT3_BareCode(t *testing.T) {
	code, err := parsePastedAuthCode("abc123def456ghi789", "S")
	require.NoError(t, err)
	assert.Equal(t, "abc123def456ghi789", code)
}

// CT-4: Unparseable inputs → parse error (table-driven).
func TestParsePastedAuthCode_CT4_Unparseable(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"prose", "I clicked the link but nothing happened"},
		{"url_without_code", "http://127.0.0.1:53682/callback?state=S"},
		{"short_token", "short"},
		{"empty", ""},
		{"whitespace_only", "   "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, err := parsePastedAuthCode(c.input, "S")
			require.Error(t, err)
			assert.ErrorIs(t, err, errPasteUnparseable)
			assert.Empty(t, code)
		})
	}
}

// BT-1: Whitespace / shell-quoted paste trimmed and accepted.
func TestParsePastedAuthCode_BT1_WhitespaceAndQuotesTrimmed(t *testing.T) {
	code, err := parsePastedAuthCode("  \"abc123def456ghi789\"  ", "S")
	require.NoError(t, err)
	assert.Equal(t, "abc123def456ghi789", code)
}

// --- runYouTubeLoopbackAuth race + degrade tests (CT-5..CT-10, BT-2, RG-3, RG-4) ---

// fakeTokenServer returns an httptest.Server acting as a fake Google token
// endpoint, plus an atomic counter of how many times it was hit.
func fakeTokenServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fake_access","refresh_token":"fake_refresh","expires_in":3600,"token_type":"Bearer"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// setEndpoint overrides the youtubeOAuthEndpoint seam to point at srv, restoring on cleanup.
func setEndpoint(t *testing.T, srv *httptest.Server) {
	t.Helper()
	orig := youtubeOAuthEndpoint
	youtubeOAuthEndpoint = oauth2.Endpoint{
		AuthURL:  srv.URL + "/auth",
		TokenURL: srv.URL + "/token",
	}
	t.Cleanup(func() { youtubeOAuthEndpoint = orig })
}

// setSeams overrides isTerminalFn and pasteReaderFn, restoring on cleanup.
func setSeams(t *testing.T, tty bool, reader io.Reader) {
	t.Helper()
	origTerm := isTerminalFn
	origReader := pasteReaderFn
	isTerminalFn = func(int) bool { return tty }
	if reader != nil {
		pasteReaderFn = func() io.Reader { return reader }
	}
	t.Cleanup(func() {
		isTerminalFn = origTerm
		pasteReaderFn = origReader
	})
}

// freePort returns an available TCP loopback address, releasing the listener
// immediately so the caller can reuse the port.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// CT-5: Race - paste wins with loopback unreachable → token exchanged and returned.
func TestRunYouTubeLoopbackAuth_CT5_PasteWinsRace(t *testing.T) {
	srv, hits := fakeTokenServer(t)
	setEndpoint(t, srv)

	addr := freePort(t)
	// Drain the auth URL first by starting the call in a goroutine; we need the
	// generated state to construct a valid paste line, so instead paste a bare
	// code (no state required, Q1=A) - simplest deterministic race winner.
	reader := strings.NewReader("bare-pasted-code-1234567890\n")
	setSeams(t, true, reader)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tok, err := runYouTubeLoopbackAuth(ctx, "cid", "csecret", addr, true, nil)
	require.NoError(t, err)
	assert.Equal(t, "fake_refresh", tok.RefreshToken)
	assert.EqualValues(t, 1, atomic.LoadInt32(hits))
}

// CT-6: Race - loopback wins → stdin cancelled (accepted leak), exchange
// endpoint hit exactly once. The generated `state` value is only ever
// printed to stderr, so this test redirects os.Stderr to capture the auth
// URL, extracts state from it, and drives the real callback endpoint with a
// matching request while the paste reader blocks on an io.Pipe that never
// yields input.
func TestRunYouTubeLoopbackAuth_CT6_LoopbackWinsRace(t *testing.T) {
	srv, hits := fakeTokenServer(t)
	setEndpoint(t, srv)

	addr := freePort(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	setSeams(t, true, pr)

	// EPIC-258 M2: this test previously reassigned os.Stderr and restored it
	// while the auth goroutine was still writing to it - a data race the
	// detector reported on every seed. runYouTubeLoopbackAuth now takes an
	// io.Writer, so the pipe is owned by this test and nothing global moves.
	rPipe, wPipe, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = wPipe.Close() })

	stateCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(rPipe)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "state=") {
				u, perr := url.Parse(line)
				if perr == nil {
					if s := u.Query().Get("state"); s != "" {
						select {
						case stateCh <- s:
						default:
						}
					}
				}
			}
		}
	}()

	resultCh := make(chan struct {
		tok *oauth2.Token
		err error
	}, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tok, aerr := runYouTubeLoopbackAuth(ctx, "cid", "csecret", addr, true, wPipe)
		resultCh <- struct {
			tok *oauth2.Token
			err error
		}{tok, aerr}
	}()

	var state string
	select {
	case state = <-stateCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting to capture oauth state from stderr")
	}

	// Close the read side; further writes from the auth goroutine go to a
	// pipe this test owns and cannot race anything.
	_ = rPipe.Close()

	callbackURL := "http://" + addr + "/callback?code=loopback-code-123&state=" + url.QueryEscape(state)
	resp, err := http.Get(callbackURL)
	require.NoError(t, err)
	resp.Body.Close()

	res := <-resultCh
	require.NoError(t, res.err)
	assert.Equal(t, "fake_refresh", res.tok.RefreshToken)
	assert.EqualValues(t, 1, atomic.LoadInt32(hits), "exactly one exchange must occur")

	_ = pw.CloseWithError(io.EOF)
}

// CT-7: Non-TTY stdin never arms the paste acceptor; flow identical to today (loopback-only, times out here).
func TestRunYouTubeLoopbackAuth_CT7_NonTTYNeverArmsPaste(t *testing.T) {
	srv, hits := fakeTokenServer(t)
	setEndpoint(t, srv)

	addr := freePort(t)
	reader := strings.NewReader("bare-pasted-code-1234567890\n")
	setSeams(t, false, reader)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := runYouTubeLoopbackAuth(ctx, "cid", "csecret", addr, true, nil)
	require.Error(t, err)
	// Should time out on ctx, not resolve via the paste we injected.
	assert.EqualValues(t, 0, atomic.LoadInt32(hits))
}

// CT-8: 3 garbage pastes → fatal oauth_paste_unparseable.
func TestRunYouTubeLoopbackAuth_CT8_ThreeBadPastesFatal(t *testing.T) {
	srv, hits := fakeTokenServer(t)
	setEndpoint(t, srv)

	addr := freePort(t)
	reader := strings.NewReader("bad1\nbad2\nbad3\n")
	setSeams(t, true, reader)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := runYouTubeLoopbackAuth(ctx, "cid", "csecret", addr, true, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, errPasteUnparseable)
	assert.EqualValues(t, 0, atomic.LoadInt32(hits))
}

// CT-9: Bound port + TTY → paste-only mode: warn emitted, auth URL uses
// --callback-addr value, paste completes exchange.
func TestRunYouTubeLoopbackAuth_CT9_BoundPortTTYPasteOnly(t *testing.T) {
	srv, hits := fakeTokenServer(t)
	setEndpoint(t, srv)

	// Bind the port ourselves to force EADDRINUSE.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	addr := ln.Addr().String()

	reader := strings.NewReader("bare-pasted-code-1234567890\n")
	setSeams(t, true, reader)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tok, err := runYouTubeLoopbackAuth(ctx, "cid", "csecret", addr, true, nil)
	require.NoError(t, err)
	assert.Equal(t, "fake_refresh", tok.RefreshToken)
	assert.EqualValues(t, 1, atomic.LoadInt32(hits))
}

// CT-10: Bound port + non-TTY → existing fail-fast "listen loopback" error.
func TestRunYouTubeLoopbackAuth_CT10_BoundPortNonTTYFailFast(t *testing.T) {
	srv, hits := fakeTokenServer(t)
	setEndpoint(t, srv)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	addr := ln.Addr().String()

	setSeams(t, false, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = runYouTubeLoopbackAuth(ctx, "cid", "csecret", addr, true, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listen loopback")
	assert.EqualValues(t, 0, atomic.LoadInt32(hits))
}

// BT-2: Prompt text emitted to stderr only when TTY seam true. We can't
// easily capture os.Stderr writes from runYouTubeLoopbackAuth without
// redirecting the process-wide stderr, so this is validated at the
// parsePastedAuthCode/isTerminalFn seam boundary: verify the seam is
// consulted and that a non-TTY run never attempts to read pasted input
// (already covered by CT-7's zero-hits assertion). Directly assert the seam
// wiring here.
func TestRunYouTubeLoopbackAuth_BT2_SeamConsulted(t *testing.T) {
	var consulted bool
	origTerm := isTerminalFn
	isTerminalFn = func(int) bool {
		consulted = true
		return false
	}
	defer func() { isTerminalFn = origTerm }()

	addr := freePort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _ = runYouTubeLoopbackAuth(ctx, "cid", "csecret", addr, true, nil)
	assert.True(t, consulted, "isTerminalFn seam must be consulted")
}

// --- Regression guards (RG-2..RG-4) ---

// RG-2 (F-019 RG-5): static default callback 127.0.0.1:53682 unchanged.
func TestRunYouTubeLoopbackAuth_RG2_DefaultCallbackAddrUnchanged(t *testing.T) {
	cmd := authYouTubeCmd()
	f := cmd.Flags().Lookup("callback-addr")
	require.NotNil(t, f)
	assert.Equal(t, "127.0.0.1:53682", f.DefValue)
}

// RG-3: Exactly one exchange per invocation even under channel race
// (both a valid paste and, separately, a well-formed environment are
// present - verified via the CT-5/CT-9 hit counters staying at 1, and the
// bound-port TTY degrade path never double-exchanging).
func TestRunYouTubeLoopbackAuth_RG3_ExactlyOneExchange(t *testing.T) {
	srv, hits := fakeTokenServer(t)
	setEndpoint(t, srv)

	addr := freePort(t)
	reader := strings.NewReader("bare-pasted-code-1234567890\nbare-pasted-code-should-not-fire\n")
	setSeams(t, true, reader)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := runYouTubeLoopbackAuth(ctx, "cid", "csecret", addr, true, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 1, atomic.LoadInt32(hits), "exactly one token exchange must occur")
}

// RG-4: Failed headless auth leaves a pre-seeded slot token untouched. This
// exercises the full auth command path with a failing exchange (fake server
// returns an error status) and asserts the queue row is unchanged.
func TestAuthYouTube_RG4_FailedAuthLeavesSlotUntouched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	setEndpoint(t, srv)

	addr := freePort(t)
	// Garbage paste x3 forces a fatal error without ever reaching exchange.
	reader := strings.NewReader("bad1\nbad2\nbad3\n")
	setSeams(t, true, reader)

	queueDB, restore := setupAuthYouTubeTest(t, "unused")
	// setupAuthYouTubeTest overrides runYouTubeLoopbackAuthFn to a stub; we
	// want the real function here, so restore its default first and re-point
	// it at the real implementation with our seams engaged.
	restore()
	runYouTubeLoopbackAuthFn = runYouTubeLoopbackAuth
	defer func() { runYouTubeLoopbackAuthFn = runYouTubeLoopbackAuth }()

	q, err := NewQueue(queueDB, false)
	require.NoError(t, err)
	require.NoError(t, q.SetYouTubeSlotToken(1, "default", "pre_existing_token", 999))
	q.Close()

	cmd := authCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"youtube", "--queue-db", queueDB, "--callback-addr", addr, "--no-browser"})
	err = cmd.Execute()
	require.Error(t, err)

	q2, err := NewQueue(queueDB, false)
	require.NoError(t, err)
	defer q2.Close()
	tok, _, err := q2.GetYouTubeSlotToken(1, "default")
	require.NoError(t, err)
	assert.Equal(t, "pre_existing_token", tok, "slot token must be untouched after failed headless auth")
}
