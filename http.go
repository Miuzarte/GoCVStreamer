package main

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime/debug"
	"time"
)

type MetricsSnapshot struct {
	CaptureFPS    float64 `json:"capture_fps"`
	CaptureCostMs float64 `json:"capture_cost_ms"`
	MatchFPS      float64 `json:"match_fps"`
	MatchCostMs   float64 `json:"match_cost_ms"`
	YoloFPS       float64 `json:"yolo_fps"`
	YoloCostMs    float64 `json:"yolo_cost_ms"`

	FramesElapsed int  `json:"frames_elapsed"`
	Debugging     bool `json:"debugging"`

	Cpu float64 `json:"cpu"`

	GcCount      int     `json:"gc_count"`
	GcPauseAvgUs float64 `json:"gc_pause_avg_us"`
	GcSinceLastS float64 `json:"gc_since_last_s"`

	MatchCostTotalMs float64 `json:"match_cost_total_ms"`
	MatchCount       int     `json:"match_count"`
	MatchCostAvgMs   float64 `json:"match_cost_avg_ms"`

	PersonDetCostMs float64 `json:"person_det_ms"`
	PersonDetCount  int     `json:"person_det_count"`

	CurrentWeapon string  `json:"current_weapon"`
	WeaponFound   bool    `json:"weapon_found"`
	WeaponVal     float32 `json:"weapon_val"`
	Idle          bool    `json:"idle"`
	Narrowing     bool    `json:"narrowing"`
}

var lastGCStats debug.GCStats

func snapshotMetrics() MetricsSnapshot {
	const ms = float64(time.Millisecond)
	const us = float64(time.Microsecond)

	var m MetricsSnapshot

	if capSrv != nil {
		s := capSrv.Stats()
		m.CaptureFPS = s.FPS
		m.CaptureCostMs = s.CostMs()
		m.FramesElapsed = s.FrameCount
	}

	if matcherEng != nil {
		s := matcherEng.Stats()
		m.MatchFPS = s.FPS
		m.MatchCostMs = s.CostMs
		m.MatchCostTotalMs = s.CostMs
		m.MatchCount = s.Matched
		if s.Matched > 0 {
			m.MatchCostAvgMs = s.CostMs / float64(s.Matched)
		}
		m.Idle = s.Idle
		m.Narrowing = s.Narrowing
		m.WeaponFound = s.Found
		m.WeaponVal = s.Confidence

		if s.Found {
			weap := eng.Weapon()
			if weap != nil {
				m.CurrentWeapon = weap.String()
			}
		}
	}

	if detEng != nil {
		results, count, cost := detEng.Snapshot()
		_ = results
		m.PersonDetCount = count
		m.PersonDetCostMs = float64(cost) / ms
	}

	m.Cpu = cpu
	m.Debugging = debugging

	debug.ReadGCStats(&lastGCStats)
	if lastGCStats.NumGC > 0 {
		m.GcCount = int(lastGCStats.NumGC)
		m.GcPauseAvgUs = float64(lastGCStats.PauseTotal) / float64(lastGCStats.NumGC) / us
		m.GcSinceLastS = time.Since(lastGCStats.LastGC).Seconds()
	}

	return m
}

func startHttpServer(ctx context.Context, addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		m := snapshotMetrics()
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
