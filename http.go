package main

import (
	"context"
	"encoding/json"
	"net/http"
)

func startHttpServer(ctx context.Context, addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		m := SnapshotMetrics()
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(m)
	})

	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	go func() {
		log.Info().Str("addr", addr).Msg("HTTP server started")
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Warn().Err(err).Msg("HTTP server error")
		}
	}()
}
