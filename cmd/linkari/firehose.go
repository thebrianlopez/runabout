package main

// EPIC-016: Bluesky Firehose Monitoring
//
// Connects to the Bluesky firehose (com.atproto.sync.subscribeRepos) via
// WebSocket, decodes CBOR commit events, and enqueues matching posts based
// on configured keyword subscriptions. Uses kind='notify' push rows  -  never
// kind='digest'  -  to avoid consuming the per-profile digest throttle window.

import (
	"bytes"
	"context"
	"encoding/binary"
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
var errCursorExpired = errors.New("relay error frame  -  cursor expired or rejected")

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

// firehoseOp is a single operation within a #commit body.
// Cid is CBOR tag 42 (DAG-CBOR CID) for create ops; nil for delete ops.
type firehoseOp struct {
	Action string          `cbor:"action"`
	Path   string          `cbor:"path"`
	Cid    cbor.RawMessage `cbor:"cid"`
}

// firehoseBody is the second element for #commit events.
type firehoseBody struct {
	Seq    int64           `cbor:"seq"`
	Repo   string          `cbor:"repo"`
	Ops    []firehoseOp    `cbor:"ops"`
	Blocks cbor.RawMessage `cbor:"blocks"`
}

// atProtoPost is the minimal post record struct for CAR block text extraction.
type atProtoPost struct {
	Type string `cbor:"$type"`
	Text string `cbor:"text"`
}

// parseCIDLen returns the byte length of a CIDv1 prefix at the start of data.
// CIDv1 format: [varint version][varint codec][varint hashfn][varint digestLen][digestLen bytes]
func parseCIDLen(data []byte) (int, error) {
	n := 0
	for i := 0; i < 3; i++ { // version, codec, hash function code
		_, size := binary.Uvarint(data[n:])
		if size <= 0 {
			return 0, errors.New("bad CID varint")
		}
		n += size
	}
	digestLen, size := binary.Uvarint(data[n:])
	if size <= 0 {
		return 0, errors.New("bad digest length varint")
	}
	n += size + int(digestLen)
	if n > len(data) {
		return 0, errors.New("CID truncated")
	}
	return n, nil
}

// extractCIDFromTag42 extracts raw CID bytes from a CBOR DAG-CBOR CID value.
// DAG-CBOR CIDs are encoded as tag(42, bytes(\x00 + raw_cid_bytes)).
// The \x00 is the identity multibase prefix per the DAG-CBOR spec.
func extractCIDFromTag42(raw cbor.RawMessage) ([]byte, error) {
	if len(raw) < 4 {
		return nil, errors.New("too short for tag42")
	}
	if raw[0] != 0xd8 || raw[1] != 0x2a {
		return nil, errors.New("not tag 42")
	}
	var bstrLen, headerSize int
	b := raw[2]
	switch {
	case b >= 0x40 && b <= 0x57: // byte string, length 0-23 in lower 5 bits
		bstrLen = int(b & 0x1f)
		headerSize = 1
	case b == 0x58: // byte string, 1-byte length follows
		bstrLen = int(raw[3])
		headerSize = 2
	default:
		return nil, fmt.Errorf("unsupported byte string header 0x%02x", b)
	}
	start := 2 + headerSize
	if len(raw) < start+bstrLen {
		return nil, errors.New("truncated CID bytes")
	}
	content := raw[start : start+bstrLen]
	if len(content) == 0 || content[0] != 0x00 {
		return nil, errors.New("missing identity prefix")
	}
	return content[1:], nil
}

// carExtractPostText extracts app.bsky.feed.post text from ATProto commit CAR v1 blocks.
// Returns a map from op path to post text, nil on any CAR parse error.
// Handles absent/empty blocks gracefully (returns nil, no panic).
func carExtractPostText(blocks []byte, ops []firehoseOp) map[string]string {
	if len(blocks) == 0 {
		return nil
	}
	// Read CAR v1 header: [varint header_len][header CBOR]
	headerLen, hSize := binary.Uvarint(blocks)
	if hSize <= 0 {
		slog.Warn("firehose car decode error",
			"event_type", "firehose_car_decode_error",
			"error_class", "firehose_car_decode_error",
			"error", "bad header varint",
		)
		return nil
	}
	n := hSize + int(headerLen)
	if n > len(blocks) {
		slog.Warn("firehose car decode error",
			"event_type", "firehose_car_decode_error",
			"error_class", "firehose_car_decode_error",
			"error", "header exceeds block data",
		)
		return nil
	}
	// Build CID bytes -> record CBOR map from sequential block entries.
	blocksByCID := make(map[string][]byte)
	for n < len(blocks) {
		blockLen, bSize := binary.Uvarint(blocks[n:])
		if bSize <= 0 {
			slog.Warn("firehose car decode error",
				"event_type", "firehose_car_decode_error",
				"error_class", "firehose_car_decode_error",
				"error", "bad block length varint",
			)
			return nil
		}
		n += bSize
		end := n + int(blockLen)
		if end > len(blocks) {
			slog.Warn("firehose car decode error",
				"event_type", "firehose_car_decode_error",
				"error_class", "firehose_car_decode_error",
				"error", "block truncated",
			)
			return nil
		}
		blockData := blocks[n:end]
		n = end
		cidLen, err := parseCIDLen(blockData)
		if err != nil || cidLen >= len(blockData) {
			slog.Warn("firehose car decode error",
				"event_type", "firehose_car_decode_error",
				"error_class", "firehose_car_decode_error",
				"error", err,
			)
			return nil
		}
		blocksByCID[string(blockData[:cidLen])] = blockData[cidLen:]
	}
	// Match op CIDs to blocks and decode post records.
	result := make(map[string]string)
	for _, op := range ops {
		if len(op.Cid) == 0 {
			continue // delete op: nil CID, no text to extract (BT-3)
		}
		cidBytes, err := extractCIDFromTag42(op.Cid)
		if err != nil {
			slog.Debug("firehose cid mismatch",
				"event_type", "firehose_cid_mismatch",
				"at_uri", op.Path,
				"ops_count", len(ops),
				"blocks_count", len(blocksByCID),
			)
			continue
		}
		recordCBOR, ok := blocksByCID[string(cidBytes)]
		if !ok {
			slog.Debug("firehose cid mismatch",
				"event_type", "firehose_cid_mismatch",
				"at_uri", op.Path,
				"ops_count", len(ops),
				"blocks_count", len(blocksByCID),
			)
			continue
		}
		var post atProtoPost
		if err := cbor.Unmarshal(recordCBOR, &post); err != nil {
			slog.Warn("firehose post decode error",
				"event_type", "firehose_post_decode_error",
				"error_class", "firehose_post_decode_error",
				"error", err,
				"at_uri", op.Path,
			)
			continue
		}
		if post.Type != "app.bsky.feed.post" {
			continue // non-post records skipped (CT-5)
		}
		result[op.Path] = post.Text
	}
	return result
}

// processFirehoseMessage decodes a raw WebSocket message and dispatches post
// records. Returns nil (skip) on malformed CBOR  -  never propagates decode errors
// so the worker stays alive. CT-4 contract.
//
// ATProto framing: each WebSocket frame contains two concatenated CBOR values
// (header map, body map)  -  NOT a CBOR array. Validated against go-indigo
// events/consumer.go:HandleRepoStream.
func processFirehoseMessage(ctx context.Context, fsc *firehoseScoreContext, msg []byte) error {
	q := fsc.Queue
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
		// the worker to reset to live tail  -  retrying the same cursor is futile.
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

	// Extract post text from CAR v1 blocks. Empty blocks are expected for
	// tooBig commits; nil rawBlocks produces a nil textByPath map (no text).
	var rawBlocks []byte
	if len(body.Blocks) > 0 {
		if err := cbor.Unmarshal(body.Blocks, &rawBlocks); err != nil {
			slog.Warn("firehose car decode error",
				"event_type", "firehose_car_decode_error",
				"error_class", "firehose_car_decode_error",
				"error", err,
			)
		}
	} else {
		slog.Debug("firehose blocks absent",
			"event_type", "firehose_blocks_absent",
			"seq", body.Seq,
		)
	}
	textByPath := carExtractPostText(rawBlocks, body.Ops)

	for _, op := range body.Ops {
		if op.Action != "create" || !strings.HasPrefix(op.Path, "app.bsky.feed.post/") {
			continue
		}
		atURI := "at://" + body.Repo + "/" + op.Path
		var text string
		if textByPath != nil {
			text = textByPath[op.Path]
		}
		if text != "" {
			slog.Debug("firehose car text extracted",
				"event_type", "firehose_car_text_extracted",
				"at_uri", atURI,
				"text_len", len(text),
				"ops_matched", len(textByPath),
			)
		}
		post := &firehosePost{
			AtURI: atURI,
			Text:  text,
			Repo:  body.Repo,
			Seq:   body.Seq,
		}
		if err := handleFirehosePost(ctx, fsc, post); err != nil {
			slog.Warn("firehose post handle error",
				"seq", body.Seq,
				"error", err,
			)
		}
	}
	return nil
}

// handleFirehosePost checks keyword subscriptions and enqueues matching posts,
// then launches scoreAsync in a goroutine bounded by fsc.ScoreSem (max 3 concurrent).
// When fsc is nil (tests or legacy callers), scoring is skipped and the row stays pending.
func handleFirehosePost(ctx context.Context, fsc *firehoseScoreContext, post *firehosePost) error {
	q := fsc.Queue

	// Dedup guard: skip if same AT URI already seen (BT-1, BT-4).
	isNew, err := q.IsNewContent("bsky_firehose", post.AtURI)
	if err != nil || !isNew {
		return nil
	}
	_ = q.MarkContentSeen("bsky_firehose", post.AtURI, 0)

	// Collect all subscriptions into a slice before doing any writes.
	// With MaxOpenConns=1, holding an open rows cursor while calling write
	// methods would deadlock  -  both need the same single connection.
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

		resolvedProfile := resolveFirehoseProfile(s.profile)
		req := &ShareRequest{
			URL:     post.AtURI,
			Text:    post.Text,
			Profile: resolvedProfile,
			Type:    "url",
			Action:  firehoseActionForProfile(resolvedProfile),
		}
		rowID, err := q.EnqueueWithSource(req, "firehose")
		if err != nil {
			slog.Warn("firehose enqueue error", "error", err)
			continue
		}
		req.QueueRowID = rowID

		slog.Info("firehose commit matched",
			"event_type", "firehose_commit_matched",
			"seq", post.Seq,
			"at_uri", post.AtURI,
			"keyword", s.keyword,
			"profile", resolvedProfile,
			"queue_id", rowID,
		)

		// Launch scoreAsync in a goroutine, bounded by the semaphore (max 3 concurrent).
		if fsc.Eval != nil {
			go func(req *ShareRequest, keyword, profile string) {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("firehose scoring goroutine panicked",
							"event_type", "firehose_scoring_panic",
							"queue_id", req.QueueRowID,
							"panic", r,
						)
						if req.QueueRowID > 0 {
							_ = q.MarkFailedWithReason(req.QueueRowID, fmt.Sprintf("panic: %v", r))
						}
					}
				}()

				// Log semaphore_wait if all 3 slots are busy (channel at capacity).
				if len(fsc.ScoreSem) == cap(fsc.ScoreSem) {
					slog.Warn("firehose scoring semaphore wait",
						"event_type", "firehose_semaphore_wait",
						"queue_id", req.QueueRowID,
						"active_goroutines", cap(fsc.ScoreSem),
					)
				}
				fsc.ScoreSem <- struct{}{}
				defer func() { <-fsc.ScoreSem }()

				start := time.Now()
				slog.Info("firehose scoring started",
					"event_type", "firehose_scoring_started",
					"queue_id", req.QueueRowID,
					"at_uri", req.URL,
					"profile", profile,
					"keyword", keyword,
				)
				scoreAsync(req, q, fsc.Eval, fsc.Events, fsc.BskyClient, nil)
				slog.Info("firehose scoring goroutine done",
					"event_type", "firehose_scoring_done",
					"queue_id", req.QueueRowID,
					"latency_ms", time.Since(start).Milliseconds(),
				)
			}(req, s.keyword, resolvedProfile)
		}
	}
	return nil
}

// BlueskyFirehoseSource wraps runFirehoseWorker behind the ContentSource interface.
type BlueskyFirehoseSource struct {
	client *BlueskyClient
	eval   Evaluator    // M4: wired at registration time
	events *EventLogger // M4: wired at registration time; nil = event logging disabled
}

// Name returns the stable source identifier used as the seen_content.source key.
func (s *BlueskyFirehoseSource) Name() string { return "bsky_firehose" }

// AuthDeps declares the auth providers required before Start() is called.
// The registry skips this source when the "bluesky" provider is not ready.
func (s *BlueskyFirehoseSource) AuthDeps() []string { return []string{"bluesky"} }

// Start runs the WebSocket loop with exponential backoff.
// Called only when AuthDeps() are satisfied  -  client is guaranteed non-nil.
func (s *BlueskyFirehoseSource) Start(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error {
	// Migrate any subscriptions still using the legacy 'default' profile (F2).
	migrateFirehoseProfiles(q)

	fsc := &firehoseScoreContext{
		Queue:      q,
		Eval:       s.eval,
		Events:     s.events,
		BskyClient: s.client,
		ScoreSem:   make(chan struct{}, 3),
	}
	runFirehoseWorker(ctx, fsc, slog.Default())
	return nil
}

// execConnectAndRead is the production WebSocket connect+read function,
// exposed as a variable so tests can inject a stub (RG-2 seam).
var execConnectAndRead = connectAndRead

// connectAndRead opens a WebSocket connection to url and reads messages until
// the context is cancelled or an error occurs.
func connectAndRead(ctx context.Context, fsc *firehoseScoreContext, url string) error {
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
		if err := processFirehoseMessage(ctx, fsc, msg); err != nil {
			if errors.Is(err, errCursorExpired) {
				return err // propagate to runFirehoseWorker for cursor reset
			}
			slog.Warn("firehose process error", "error", err)
		}
	}
}

// runFirehoseWorker connects to the Bluesky firehose and processes commits.
// Reconnects with exponential backoff (1s → 5min max). Exits only on ctx.Done().
func runFirehoseWorker(ctx context.Context, fsc *firehoseScoreContext, logger *slog.Logger) {
	q := fsc.Queue
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

		err := execConnectAndRead(ctx, fsc, connectURL)
		if ctx.Err() != nil {
			return // context cancelled  -  normal shutdown
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
