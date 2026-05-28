package main

// EPIC-015: Bluesky Verdict Replies.
//
// publishVerdictReply posts a Linkari score verdict as a threaded reply via the
// AT Protocol after a share is scored. Critical invariant: failure here MUST
// NOT block FCM push delivery — all error paths log WARN and return nil.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// execPublishReply and execGetRecord are package-level seams for testing.
// Production code uses defaultExecPublishReply / defaultExecGetRecord.
var (
	execPublishReply = defaultExecPublishReply
	execGetRecord    = defaultExecGetRecord
)

type bskyReplyRef struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

type bskyReplyRecord struct {
	Type      string     `json:"$type"`
	Text      string     `json:"text"`
	Reply     *bskyReply `json:"reply,omitempty"`
	CreatedAt string     `json:"createdAt"`
}

type bskyReply struct {
	Root   bskyReplyRef `json:"root"`
	Parent bskyReplyRef `json:"parent"`
}

type bskyCreateRecordReq struct {
	Repo       string          `json:"repo"`
	Collection string          `json:"collection"`
	Record     bskyReplyRecord `json:"record"`
}

type bskyGetRecordResp struct {
	CID string `json:"cid"`
}

// buildReplyText formats the verdict reply body.
func buildReplyText(score int, verdict string) string {
	return fmt.Sprintf("Linkari: %s \u2014 %d/100", verdict, score)
}

// isATURI returns true if the URL is an AT Protocol URI.
func isATURI(url string) bool {
	return strings.HasPrefix(url, "at://")
}

// publishVerdictReply posts a Linkari verdict as a Bluesky reply.
// All failure paths log and return nil — this function never blocks FCM delivery.
// EPIC-015 M3.
func publishVerdictReply(ctx context.Context, client *BlueskyClient, atURI string, score int, verdict string, q *Queue, userID int64) error {
	if !isATURI(atURI) {
		slog.Debug(
			"bluesky reply skipped: not at:// URI",
			"event_type", "bluesky_reply_skipped_opt_out",
			"row_id", userID,
		)
		return nil
	}
	optIn, err := q.GetBlueskyPublishOptIn(userID)
	if err != nil || !optIn {
		slog.Debug(
			"bluesky reply skipped: opt out",
			"event_type", "bluesky_reply_skipped_opt_out",
			"row_id", userID,
		)
		return nil
	}
	if client == nil {
		slog.Warn(
			"bluesky reply skipped: session missing",
			"event_type", "bluesky_reply_skipped_session_missing",
			"row_id", userID,
			"error_class", "bluesky_session_missing",
		)
		return nil
	}

	cid, err := execGetRecord(ctx, client, atURI)
	if err != nil {
		if strings.Contains(err.Error(), "bluesky_post_not_found") {
			slog.Warn(
				"bluesky reply: post not found",
				"event_type", "bluesky_reply_failed",
				"row_id", userID,
				"error_class", "bluesky_post_not_found",
				"error", err,
			)
			return nil
		}
		slog.Warn(
			"bluesky reply failed",
			"event_type", "bluesky_reply_failed",
			"row_id", userID,
			"error_class", "bluesky_getrecord_failed",
			"error", err,
		)
		return nil
	}

	if err := execPublishReply(ctx, client, atURI, cid, verdict, "", score); err != nil {
		if strings.Contains(err.Error(), "RateLimitExceeded") {
			slog.Warn(
				"bluesky reply rate limited",
				"event_type", "bluesky_reply_rate_limited",
				"row_id", userID,
				"retry_after_secs", 60,
			)
			return nil
		}
		slog.Warn(
			"bluesky reply failed",
			"event_type", "bluesky_reply_failed",
			"row_id", userID,
			"error_class", "bluesky_reply_error",
			"error", err,
		)
		return nil
	}

	slog.Info(
		"bluesky reply published",
		"event_type", "bluesky_reply_published",
		"row_id", userID,
		"account_did", client.AccountDID(),
		"score", score,
	)
	return nil
}

func defaultExecGetRecord(ctx context.Context, client *BlueskyClient, atURI string) (string, error) {
	// Parse at://did/collection/rkey
	parts := strings.SplitN(strings.TrimPrefix(atURI, "at://"), "/", 3)
	if len(parts) != 3 {
		return "", fmt.Errorf("bluesky_post_not_found: invalid AT URI %q", atURI)
	}
	repo, collection, rkey := parts[0], parts[1], parts[2]
	url := client.Session.host() + "/xrpc/com.atproto.repo.getRecord" +
		"?repo=" + repo + "&collection=" + collection + "&rkey=" + rkey

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+client.Session.AccessJWT)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", errors.New("bluesky_post_not_found")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("getRecord status %d", resp.StatusCode)
	}
	var result bskyGetRecordResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.CID, nil
}

func defaultExecPublishReply(ctx context.Context, client *BlueskyClient, atURI, cid, verdict, replyURI string, score int) error {
	ref := bskyReplyRef{URI: atURI, CID: cid}
	record := bskyReplyRecord{
		Type:      "app.bsky.feed.post",
		Text:      buildReplyText(score, verdict),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Reply: &bskyReply{
			Root:   ref,
			Parent: ref,
		},
	}
	body, _ := json.Marshal(bskyCreateRecordReq{
		Repo:       client.Session.DID,
		Collection: "app.bsky.feed.post",
		Record:     record,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		client.Session.host()+"/xrpc/com.atproto.repo.createRecord",
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+client.Session.AccessJWT)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return errors.New("RateLimitExceeded")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("createRecord status %d", resp.StatusCode)
	}
	return nil
}
