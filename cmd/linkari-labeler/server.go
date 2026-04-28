package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

type labelerServer struct {
	cfg  *LabelerConfig
	db   *ReadOnlyDB
	rule PostRuleFunc
}

func newLabelerServer(cfg *LabelerConfig, db *ReadOnlyDB) *labelerServer {
	return &labelerServer{
		cfg:  cfg,
		db:   db,
		rule: makeLinkariScoreRule(db),
	}
}

func (s *labelerServer) handleQueryLabels(w http.ResponseWriter, r *http.Request) {
	atURI := r.URL.Query().Get("uris")
	if atURI == "" {
		http.Error(w, "missing uris param", http.StatusBadRequest)
		return
	}

	var labels []map[string]string
	ctx := &xrpcRuleCtx{uri: atURI}
	s.rule(ctx, atURI)
	for _, l := range ctx.labels {
		labels = append(labels, map[string]string{"uri": atURI, "val": l})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"labels": labels})
}

type xrpcRuleCtx struct {
	uri    string
	labels []string
}

func (x *xrpcRuleCtx) AtURI() string           { return x.uri }
func (x *xrpcRuleCtx) AddRecordLabel(l string) { x.labels = append(x.labels, l) }

func (s *labelerServer) run(ctx context.Context) error {
	slog.Info("labeler started",
		"event_type", "labeler_started",
		"labeler_did", s.cfg.LabelerDID,
		"listen_addr", s.cfg.ListenAddr,
		"queue_db_path", s.cfg.QueueDBPath,
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/xrpc/app.bsky.labeler.queryLabels", s.handleQueryLabels)

	srv := &http.Server{Addr: s.cfg.ListenAddr, Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background()) //nolint:errcheck
	}()
	return srv.ListenAndServe()
}
