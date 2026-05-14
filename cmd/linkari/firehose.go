package main

// EPIC-016: Bluesky Firehose Monitoring
//
// Connects to the Bluesky firehose (com.atproto.sync.subscribeRepos) via
// WebSocket, decodes CBOR commit events, and enqueues matching posts based
// on configured keyword subscriptions. Uses kind='notify' push rows — never
// kind='digest' — to avoid consuming the per-profile digest throttle window.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/fxamacker/cbor/v2"
)

// errCursorExpired is returned by processFirehoseMessage when the relay sends
// an op=-1 error frame. runFirehoseWorker resets to live tail (cursor=0) on
// this signal rather than retrying with the same stale cursor indefinitely.
var errCursorExpired = errors.New("relay error frame — cursor expired or rejected")

// firehosePost represents a decoded Bluesky post from the firehose.
type firehosePost struct {
	AtURI string
	Text  string
	Repo  string
	Seq   int64
}

// firehoseHeader is the first of two concatenated CBOR values in an ATProto frame.
// op=1 is a normal message; op=-1 is an error frame.
type firehoseHeader struct {
	Op int64  `cbor:"op"`
	T  string `cbor:"t"`
}

// firehoseBody is the second element for #commit events.
type firehoseBody struct {
	Seq  int64  `cbor:"seq"`
	Repo string `cbor:"repo"`
	Ops  []struct {
		Action string `cbor:"action"`
		Path   string `cbor:"path"`
	} `cbor:"ops"`
}

// processFirehoseMessage decodes a raw WebSocket message and dispatches post
// records. Returns nil (skip) on malformed CBOR — never propagates decode errors
// so the worker stays alive. CT-4 contract.
//
// ATProto framing: each WebSocket frame contains two concatenated CBOR values
// (header map, body map) — NOT a CBOR array. Validated against go-indigo
// events/consumer.go:HandleRepoStream.
func processFirehoseMessage(ctx context.Context, q *Queue, msg []byte) error {
	dec := cbor.NewDecoder(bytes.NewReader(msg))

	var header firehoseHeader
	if err := dec.Decode(&header); err != nil {
		slog.Warn("firehose decode error",
			"event_type", "firehose_decode_error",
			"error_class", "header_decode",
		)
		return nil // CT-4: skip malformed
	}
	if header.Op != 1 {
		// Relay error frame (op=-1). Decode body for observability, then signal
		// the worker to reset to live tail — retrying the same cursor is futile.
		var relayErr struct {
			Error   string `cbor:"error"`
			Message string `cbor:"message"`
		}
		_ = dec.Decode(&relayErr)
		slog.Warn("firehose relay error frame",
			"event_type", "firehose_relay_error",
			"relay_error", relayErr.Error,
			"relay_message", relayErr.Message,
		)
		return errCursorExpired
	}
	if header.T != "#commit" {
		slog.Debug("firehose frame skipped",
			"event_type", "firehose_frame_skipped",
			"frame_type", header.T,
		)
		return nil
	}

	var body firehoseBody
	if err := dec.Decode(&body); err != nil {
		slog.Warn("firehose decode error",
			"event_type", "firehose_decode_error",
			"error_class", "body_decode",
		)
		return nil
	}

	// Persist seq for cursor resume (best-effort).
	_ = q.PersistFirehoseSeq(body.Seq, nil)

	for _, op := range body.Ops {
		if op.Action != "create" || !strings.Contains(op.Path, "app.bsky.feed.post") {
			continue
		}
		// MVP: use repo+path as AT URI. Full text requires CAR block parsing.
		atURI := "at://" + body.Repo + "/" + op.Path
		post := &firehosePost{
			AtURI: atURI,
			Text:  "", // CAR block text extraction deferred to full impl
			Repo:  body.Repo,
			Seq:   body.Seq,
		}
		if err := handleFirehosePost(ctx, q, post); err != nil {
			slog.Warn("firehose post handle error",
				"seq", body.Seq,
				"error", err,
			)
		}
	}
	return nil
}

// handleFirehosePost checks keyword subscriptions and enqueues matching posts.
// Uses kind='notify' push rows — NOT kind='digest' — per EPIC-016 invariant.
func handleFirehosePost(ctx context.Context, q *Queue, post *firehosePost) error {
	// Dedup guard: skip if same AT URI already seen (BT-1, BT-4).
	isNew, err := q.IsNewContent("bsky_firehose", post.AtURI)
	if err != nil || !isNew {
		return nil
	}
	_ = q.MarkContentSeen("bsky_firehose", post.AtURI, 0)

	// Collect all subscriptions into a slice before doing any writes.
	// With MaxOpenConns=1, holding an open rows cursor while calling write
	// methods would deadlock — both need the same single connection.
	rows, err := q.db.Query("SELECT profile, keyword FROM firehose_subscriptions")
	if err != nil {
		return err
	}
	type subscription struct{ profile, keyword string }
	var subs []subscription
	for rows.Next() {
		var s subscription
		if err := rows.Scan(&s.profile, &s.keyword); err != nil {
			continue
		}
		subs = append(subs, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	textLower := strings.ToLower(post.Text + " " + post.AtURI)
	for _, s := range subs {
		if !strings.Contains(textLower, s.keyword) {
			continue
		}

		req := &ShareRequest{
			URL:     post.AtURI,
			Profile: s.profile,
			Type:    "url",
		}
		rowID, err := q.EnqueueWithSource(req, "firehose")
		if err != nil {
			slog.Warn("firehose enqueue error", "error", err)
			continue
		}

		// Enqueue notify push — NOT digest — to avoid throttle window consumption.
		_, err = q.EnqueuePushWithProfile("notify", s.profile, 0,
			fmt.Sprintf("firehose-%d", rowID), "Firehose Match", post.AtURI, "")
		if err != nil {
			slog.Warn("firehose push error", "error", err)
		}
		// Mark the queue row relayed immediately — the push notification above is
		// the delivery mechanism. Without this, StartReplay picks up the row and
		// fails with "no action for \"\"" since firehose rows carry no action.
		q.MarkRelayed(rowID)
		slog.Info("firehose commit matched",
			"event_type", "firehose_commit_matched",
			"seq", post.Seq,
			"at_uri", post.AtURI,
			"keyword", s.keyword,
			"profile", s.profile,
			"queue_id", rowID,
		)
	}
	return nil
}

// BlueskyFirehoseSource wraps runFirehoseWorker behind the ContentSource interface.
type BlueskyFirehoseSource struct {
	client *BlueskyClient
}

// Name returns the stable source identifier used as the seen_content.source key.
func (s *BlueskyFirehoseSource) Name() string { return "bsky_firehose" }

// AuthDeps declares the auth providers required before Start() is called.
// The registry skips this source when the "bluesky" provider is not ready.
func (s *BlueskyFirehoseSource) AuthDeps() []string { return []string{"bluesky"} }

// Start runs the WebSocket loop with exponential backoff.
// Called only when AuthDeps() are satisfied — client is guaranteed non-nil.
func (s *BlueskyFirehoseSource) Start(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error {
	runFirehoseWorker(ctx, q, s.client, slog.Default())
	return nil
}

// execConnectAndRead is the production WebSocket connect+read function,
// exposed as a variable so tests can inject a stub (RG-2 seam).
var execConnectAndRead = connectAndRead

// connectAndRead opens a WebSocket connection to url and reads messages until
// the context is cancelled or an error occurs.
func connectAndRead(ctx context.Context, q *Queue, url string) error {
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	slog.Info("firehose connected",
		"event_type", "firehose_connected",
		"relay_url", url,
	)

	// Emit frame throughput every 60s so a zero-throughput connected firehose
	// is observable without waiting for disconnect (POMO: firehose-cbor-decode-failure item 4).
	var framesDecoded atomic.Int64
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				slog.Info("firehose throughput",
					"event_type", "firehose_frames_decoded",
					"frames_last_60s", framesDecoded.Swap(0),
				)
			case <-done:
				return
			}
		}
	}()

	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // normal shutdown
			}
			return err
		}
		framesDecoded.Add(1)
		if err := processFirehoseMessage(ctx, q, msg); err != nil {
			if errors.Is(err, errCursorExpired) {
				return err // propagate to runFirehoseWorker for cursor reset
			}
			slog.Warn("firehose process error", "error", err)
		}
	}
}

// runFirehoseWorker connects to the Bluesky firehose and processes commits.
// Reconnects with exponential backoff (1s → 5min max). Exits only on ctx.Done().
// M5: started by serve command when bskyClient != nil.
func runFirehoseWorker(ctx context.Context, q *Queue, bskyClient *BlueskyClient, logger *slog.Logger) {
	relayURL := "wss://bsky.network/xrpc/com.atproto.sync.subscribeRepos"
	backoff := time.Second
	maxBackoff := 5 * time.Minute

	lastSeq, _ := q.LoadLastFirehoseSeq()
	connectURL := relayURL
	if lastSeq > 0 {
		connectURL = fmt.Sprintf("%s?cursor=%d", relayURL, lastSeq)
	}
	slog.Info("firehose worker started",
		"event_type", "source_start",
		"source", "bsky_firehose",
		"relay_url", connectURL,
		"cursor", lastSeq,
	)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := execConnectAndRead(ctx, q, connectURL)
		if ctx.Err() != nil {
			return // context cancelled — normal shutdown
		}

		if err != nil {
			errorClass := "websocket_error"
			if errors.Is(err, errCursorExpired) {
				errorClass = "cursor_expired"
			}
			slog.Warn("firehose disconnected",
				"event_type", "firehose_disconnected",
				"error_class", errorClass,
				"backoff_secs", int(backoff.Seconds()),
			)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff = time.Duration(math.Min(float64(backoff*2), float64(maxBackoff)))

		// Update cursor for reconnect. On cursor expiry reset to live tail (0)
		// so the relay doesn't reject the connection again with the same stale seq.
		if errors.Is(err, errCursorExpired) {
			slog.Info("firehose cursor reset to live tail",
				"event_type", "firehose_cursor_reset",
				"old_cursor", lastSeq,
			)
			lastSeq = 0
		} else {
			lastSeq, _ = q.LoadLastFirehoseSeq()
		}
		connectURL = relayURL
		if lastSeq > 0 {
			connectURL = fmt.Sprintf("%s?cursor=%d", relayURL, lastSeq)
		}
	}
}
