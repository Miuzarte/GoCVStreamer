package main

import (
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"net/http"
	"runtime/debug"
	"time"
)

type MetricsSnapshot struct {
	CaptureFps    float64 `json:"capture_fps"`
	CaptureCostMs float64 `json:"capture_cost_ms"`
	FramesElapsed int     `json:"frames_elapsed"`

	MatchFps       float64 `json:"match_fps"`
	MatchCostMs    float64 `json:"match_cost_ms"`
	MatchCount     int     `json:"match_count"`
	MatchCostAvgMs float64 `json:"match_cost_avg_ms"`

	Idle          bool    `json:"idle"`
	Narrowing     bool    `json:"narrowing"`
	WeaponFound   bool    `json:"weapon_found"`
	WeaponVal     float32 `json:"weapon_val"`
	CurrentWeapon string  `json:"current_weapon"`

	DetectionFps    float64 `json:"detection_fps"`
	DetectionCostMs float64 `json:"detection_cost_ms"`
	DetectionCount  int     `json:"detection_count"`

	StreamClients     int     `json:"stream_clients"`
	StreamFps         float64 `json:"stream_fps"`
	StreamFramesSent  uint64  `json:"stream_frames_sent"`
	StreamDetections  uint64  `json:"stream_detections"`
	StreamLastCount   int     `json:"stream_last_count"`
	StreamLatencyMs   float64 `json:"stream_latency_ms"`
	StreamInferenceMs float64 `json:"stream_inference_ms"`
	StreamNetworkMs   float64 `json:"stream_network_ms"`
	StreamFresh       bool    `json:"stream_fresh"`

	Cpu       float64 `json:"cpu"`
	Debugging bool    `json:"debugging"`

	GcCount      int     `json:"gc_count"`
	GcPauseAvgUs float64 `json:"gc_pause_avg_us"`
	GcSinceLastS float64 `json:"gc_since_last_s"`
}

var lastGCStats debug.GCStats

func snapshotMetrics() (m MetricsSnapshot) {
	const ms = float64(time.Millisecond)
	const us = float64(time.Microsecond)

	if capturerServer != nil {
		s := capturerServer.Stats()
		m.CaptureFps = s.FPS
		m.CaptureCostMs = float64(s.Cost) / ms
		m.FramesElapsed = s.FrameCount
	}

	if matcherEngine != nil {
		s := matcherEngine.Stats()
		m.MatchFps = s.Fps
		m.MatchCostMs = float64(s.Cost) / ms
		m.MatchCount = s.Matched
		if s.Matched > 0 {
			m.MatchCostAvgMs = m.MatchCostMs / float64(s.Matched)
		}
		m.Idle = s.Idle
		m.Narrowing = s.Narrowing
		m.WeaponFound = s.Found
		m.WeaponVal = s.Confidence

		if s.Found {
			res := matcherEngine.Result()
			idx := res.WeaponIndex
			wps := matcherEngine.Weapons()
			if idx >= 0 && idx < len(wps) {
				m.CurrentWeapon = wps[idx].String()
			}
		}
	}

	if detectorEngine != nil {
		s := detectorEngine.Stats()
		m.DetectionFps = s.Fps
		m.DetectionCostMs = float64(s.Cost) / ms
	}
	for _, src := range inferenceSources {
		results, _, fresh := src.Snapshot()
		if fresh {
			m.DetectionCount += len(results)
		}
	}

	if streamServer != nil {
		s := streamServer.Stats()
		m.StreamClients = s.Clients
		m.StreamFps = s.Fps
		m.StreamFramesSent = s.FramesSent
		m.StreamDetections = s.Detections
		if remoteSource != nil {
			if _, _, fresh := remoteSource.Snapshot(); fresh {
				m.StreamFresh = true
				m.StreamLastCount = s.LastCount
				m.StreamLatencyMs = float64(s.LastLatency) / ms
				m.StreamInferenceMs = float64(s.LastInference) / ms
				m.StreamNetworkMs = m.StreamLatencyMs - m.StreamInferenceMs
				if m.StreamNetworkMs < 0 {
					m.StreamNetworkMs = 0
				}
			}
		}
	}

	m.Cpu = cpu
	m.Debugging = debugging

	debug.ReadGCStats(&lastGCStats)
	if lastGCStats.NumGC > 0 {
		m.GcCount = int(lastGCStats.NumGC)
		if lastGCStats.NumGC > 0 {
			m.GcPauseAvgUs = float64(lastGCStats.PauseTotal) / float64(lastGCStats.NumGC) / us
		}
		m.GcSinceLastS = time.Since(lastGCStats.LastGC).Seconds()
	}

	return
}

func startHttpServer(ctx context.Context, addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		m := snapshotMetrics()
		w.Header().Set("Content-Type", "application/json")
		data, err := jsonv2.Marshal(m, jsontext.WithIndent("  "))
		if err != nil {
			http.Error(w, "marshal metrics: "+err.Error(), http.StatusInternalServerError)
			return
		}
		data = append(data, '\n')
		_, _ = w.Write(data)
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
