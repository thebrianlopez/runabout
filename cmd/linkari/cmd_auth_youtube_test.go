package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// fakeToken returns a deterministic oauth2.Token for tests.
func fakeToken(refreshToken string) *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  "fake_access",
		RefreshToken: refreshToken,
		Expiry:       time.Now().Add(time.Hour),
	}
}

// setupAuthYouTubeTest creates a temp queue DB and config file, overrides the
// OAuth seam to return the given token, and returns a cleanup func.
func setupAuthYouTubeTest(t *testing.T, refreshToken string) (queueDB string, restore func()) {
	t.Helper()
	dir := t.TempDir()
	queueDB = filepath.Join(dir, "queue.db")

	// Write a minimal config.toml so LoadConfig succeeds.
	cfgPath := filepath.Join(dir, "config.toml")
	cfgContent := `[server]
google_client_id = "test_client_id"
google_client_secret = "test_client_secret"
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfgContent), 0o600))
	t.Setenv("LINKARI_CONFIG", cfgPath)

	// Override OAuth seam.
	orig := runYouTubeLoopbackAuthFn
	runYouTubeLoopbackAuthFn = func(_ context.Context, _, _, _ string, _ bool, _ io.Writer, _ authIODeps) (*oauth2.Token, error) {
		return fakeToken(refreshToken), nil
	}
	restore = func() { runYouTubeLoopbackAuthFn = orig }
	return queueDB, restore
}

// CT-4: --slot personal writes to youtube_oauth_slots with slot_name="personal";
// "default" slot is unaffected.
func TestAuthYouTube_CT4_SlotPersonalWritesPersonalSlot(t *testing.T) {
	queueDB, restore := setupAuthYouTubeTest(t, "rt_personal")
	defer restore()

	// Pre-seed a "default" slot so we can verify isolation.
	q, err := NewQueue(queueDB, false)
	require.NoError(t, err)
	require.NoError(t, q.SetYouTubeSlotToken(1, "default", "original_default", 999))
	q.Close()

	cmd := authCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"youtube", "--queue-db", queueDB, "--slot", "personal", "--no-browser"})
	require.NoError(t, cmd.Execute())

	// Verify personal slot was written.
	q2, err := NewQueue(queueDB, false)
	require.NoError(t, err)
	defer q2.Close()

	personalTok, _, err := q2.GetYouTubeSlotToken(1, "personal")
	require.NoError(t, err)
	assert.Equal(t, "rt_personal", personalTok)

	// Verify default slot was not modified.
	defaultTok, _, err := q2.GetYouTubeSlotToken(1, "default")
	require.NoError(t, err)
	assert.Equal(t, "original_default", defaultTok, "default slot must be unaffected by --slot personal")

	assert.Contains(t, out.String(), `"personal"`, "output should mention the slot name")
}

// CT-4b: no --slot flag writes to slot "default" (backward-compat).
func TestAuthYouTube_CT4b_DefaultSlotBackwardCompat(t *testing.T) {
	queueDB, restore := setupAuthYouTubeTest(t, "rt_default")
	defer restore()

	cmd := authCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"youtube", "--queue-db", queueDB, "--no-browser"})
	require.NoError(t, cmd.Execute())

	q, err := NewQueue(queueDB, false)
	require.NoError(t, err)
	defer q.Close()

	tok, _, err := q.GetYouTubeSlotToken(1, "default")
	require.NoError(t, err)
	assert.Equal(t, "rt_default", tok)
}

// CT-4c: --slot "" returns non-zero exit and error before OAuth flow.
func TestAuthYouTube_CT4c_EmptySlotRejected(t *testing.T) {
	origFn := runYouTubeLoopbackAuthFn
	oauthCalled := false
	runYouTubeLoopbackAuthFn = func(_ context.Context, _, _, _ string, _ bool, _ io.Writer, _ authIODeps) (*oauth2.Token, error) {
		oauthCalled = true
		return fakeToken("should_not_reach"), nil
	}
	defer func() { runYouTubeLoopbackAuthFn = origFn }()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`[server]
google_client_id = "id"
google_client_secret = "secret"
`), 0o600))
	t.Setenv("LINKARI_CONFIG", cfgPath)
	queueDB := filepath.Join(dir, "queue.db")

	cmd := authCmd()
	cmd.SetArgs([]string{"youtube", "--queue-db", queueDB, "--slot", "", "--no-browser"})
	err := cmd.Execute()
	require.Error(t, err, "empty slot name should fail")
	assert.Contains(t, err.Error(), "[a-zA-Z0-9-]+")
	assert.False(t, oauthCalled, "OAuth flow must not start for invalid slot name")
}

// CT-4d: --slot "my slot" (space in name) returns non-zero exit before OAuth.
func TestAuthYouTube_CT4d_SpaceInSlotRejected(t *testing.T) {
	origFn := runYouTubeLoopbackAuthFn
	oauthCalled := false
	runYouTubeLoopbackAuthFn = func(_ context.Context, _, _, _ string, _ bool, _ io.Writer, _ authIODeps) (*oauth2.Token, error) {
		oauthCalled = true
		return fakeToken("nope"), nil
	}
	defer func() { runYouTubeLoopbackAuthFn = origFn }()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`[server]
google_client_id = "id"
google_client_secret = "secret"
`), 0o600))
	t.Setenv("LINKARI_CONFIG", cfgPath)
	queueDB := filepath.Join(dir, "queue.db")

	cmd := authCmd()
	cmd.SetArgs([]string{"youtube", "--queue-db", queueDB, "--slot", "my slot", "--no-browser"})
	err := cmd.Execute()
	require.Error(t, err, "slot name with space should fail")
	assert.Contains(t, err.Error(), "[a-zA-Z0-9-]+")
	assert.False(t, oauthCalled, "OAuth flow must not start for invalid slot name")
}

// RG-4: Redirect URI is fixed at port 53682 (not random).
func TestAuthYouTube_RG4_RedirectURIFixed(t *testing.T) {
	cmd := authYouTubeCmd(defaultAuthIODeps())
	callbackFlag := cmd.Flags().Lookup("callback-addr")
	require.NotNil(t, callbackFlag, "callback-addr flag must exist")
	assert.Equal(t, "127.0.0.1:53682", callbackFlag.DefValue,
		"default callback address must be fixed port 53682")

	// Also verify no dynamic port logic exists in the command source.
	src, err := os.ReadFile(filepath.Join("cmd_auth_youtube.go"))
	if err != nil {
		// Running from test dir - look relative.
		src, err = os.ReadFile("cmd_auth_youtube.go")
	}
	if err == nil {
		assert.False(t, strings.Contains(string(src), "rand.Intn"),
			"no dynamic port allocation should exist in cmd_auth_youtube.go")
	}
}

// RG-5: --slot personal does not modify slot "default".
func TestAuthYouTube_RG5_PersonalSlotDoesNotTouchDefault(t *testing.T) {
	queueDB, restore := setupAuthYouTubeTest(t, "rt_personal_v2")
	defer restore()

	// Pre-seed default slot.
	q, err := NewQueue(queueDB, false)
	require.NoError(t, err)
	require.NoError(t, q.SetYouTubeSlotToken(1, "default", "original_default_v2", 500))
	q.Close()

	cmd := authCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"youtube", "--queue-db", queueDB, "--slot", "personal", "--no-browser"})
	require.NoError(t, cmd.Execute())

	q2, err := NewQueue(queueDB, false)
	require.NoError(t, err)
	defer q2.Close()

	defaultTok, _, err := q2.GetYouTubeSlotToken(1, "default")
	require.NoError(t, err)
	assert.Equal(t, "original_default_v2", defaultTok)
}

// RG-6: Second auth for same slot updates token in place (no duplicate row).
func TestAuthYouTube_RG6_SecondAuthUpdatesInPlace(t *testing.T) {
	queueDB, restore := setupAuthYouTubeTest(t, "rt_first")
	defer restore()

	// First auth.
	cmd := authCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"youtube", "--queue-db", queueDB, "--slot", "personal", "--no-browser"})
	require.NoError(t, cmd.Execute())

	// Override the seam to return a different token for the second auth.
	origFn := runYouTubeLoopbackAuthFn
	runYouTubeLoopbackAuthFn = func(_ context.Context, _, _, _ string, _ bool, _ io.Writer, _ authIODeps) (*oauth2.Token, error) {
		return fakeToken("rt_second"), nil
	}
	defer func() { runYouTubeLoopbackAuthFn = origFn }()

	// Second auth.
	cmd2 := authCmd()
	cmd2.SetOut(new(bytes.Buffer))
	cmd2.SetErr(new(bytes.Buffer))
	cmd2.SetArgs([]string{"youtube", "--queue-db", queueDB, "--slot", "personal", "--no-browser"})
	require.NoError(t, cmd2.Execute())

	q, err := NewQueue(queueDB, false)
	require.NoError(t, err)
	defer q.Close()

	var count int
	err = q.db.QueryRow(`SELECT COUNT(*) FROM youtube_oauth_slots WHERE slot_name='personal'`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "should have exactly 1 row after two auths for same slot")

	tok, _, err := q.GetYouTubeSlotToken(1, "personal")
	require.NoError(t, err)
	assert.Equal(t, "rt_second", tok, "second auth should update token in place")
}
