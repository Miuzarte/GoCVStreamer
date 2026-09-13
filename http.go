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
	// DetectionAgeMs 为最新本地结果对应帧的帧龄 (采集 -> 现在), 即 assist 看到的"信息有多旧"
	DetectionAgeMs      float64 `json:"detection_age_ms"`
	DetectionPipelineMs float64 `json:"detection_pipeline_ms"`
	DetectionIntervalMs float64 `json:"detection_interval_ms"`

	AssistAgeMs float64 `json:"assist_age_ms"`
	AssistGated uint64  `json:"assist_gated"`

	StreamClients     int     `json:"stream_clients"`
	StreamFps         float64 `json:"stream_fps"`
	StreamFramesSent  uint64  `json:"stream_frames_sent"`
	StreamDetections  uint64  `json:"stream_detections"`
	StreamLastCount   int     `json:"stream_last_count"`
	StreamLatencyMs   float64 `json:"stream_latency_ms"`
	StreamInferenceMs float64 `json:"stream_inference_ms"`
	// StreamCpuMs 为手机侧 Execute 之外的 CPU 耗时 (解码/量化/后处理),
	// 它不属于网络, 计算 StreamNetworkMs 时必须减掉
	StreamCpuMs    float64 `json:"stream_cpu_ms"`
	StreamDecodeMs float64 `json:"stream_decode_ms"`
	StreamQuantMs  float64 `json:"stream_quant_ms"`
	StreamPostMs   float64 `json:"stream_post_ms"`
	// StreamReadMs 为手机等待下一帧的时间: 持续 ≈0 说明手机侧已饱和 (在排队)
	StreamReadMs float64 `json:"stream_read_ms"`
	// StreamNetworkMs 为真正的链路 + PC 侧耗时 = 全链路延迟 - 手机推理 - 手机 CPU
	StreamNetworkMs float64 `json:"stream_network_ms"`
	// 平均口径 (EMA, 约 1s 窗口): 单帧值噪声 ±3ms, A/B 只看这几个
	StreamLatencyAvgMs   float64 `json:"stream_latency_avg_ms"`
	StreamInferenceAvgMs float64 `json:"stream_inference_avg_ms"`
	StreamCpuAvgMs       float64 `json:"stream_cpu_avg_ms"`
	StreamNetworkAvgMs   float64 `json:"stream_network_avg_ms"`
	StreamQueueAvgMs     float64 `json:"stream_queue_avg_ms"`
	StreamFrameBytes     float64 `json:"stream_frame_bytes"`
	StreamFresh          bool    `json:"stream_fresh"`
	StreamAgeMs          float64 `json:"stream_age_ms"`

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
		if !s.At.IsZero() {
			m.DetectionAgeMs = float64(time.Since(s.At)) / ms
		}
		m.DetectionPipelineMs = float64(s.Pipeline) / ms
		m.DetectionIntervalMs = float64(s.Interval) / ms
	}
	for _, src := range inferenceSources {
		results, _, fresh := src.Snapshot()
		if fresh {
			m.DetectionCount += len(results)
		}
	}

	if assistEngine != nil {
		age, gated := assistEngine.Status()
		m.AssistAgeMs = float64(age) / ms
		m.AssistGated = gated
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
				m.StreamCpuMs = float64(s.LastCpu) / ms
				m.StreamDecodeMs = float64(s.LastDecode) / ms
				m.StreamQuantMs = float64(s.LastQuant) / ms
				m.StreamPostMs = float64(s.LastPost) / ms
				m.StreamReadMs = float64(s.LastRead) / ms
				// 手机自己的 CPU 阶段 (解码/量化/后处理) 不是网络: 减掉才是真正的链路耗时
				// 手机不上报 cpu_ms 时 (老客户端) 这里退化成 latency - inference
				m.StreamNetworkMs = m.StreamLatencyMs - m.StreamInferenceMs - m.StreamCpuMs
				if m.StreamNetworkMs < 0 {
					m.StreamNetworkMs = 0
				}
				// 平均口径: 单帧值噪声大, A/B 比较只看这几项
				m.StreamLatencyAvgMs = float64(s.AvgLatency) / ms
				m.StreamInferenceAvgMs = float64(s.AvgInference) / ms
				m.StreamCpuAvgMs = float64(s.AvgCpu) / ms
				m.StreamQueueAvgMs = float64(s.AvgQueue) / ms
				m.StreamFrameBytes = s.AvgBytes
				m.StreamNetworkAvgMs = m.StreamLatencyAvgMs - m.StreamInferenceAvgMs - m.StreamCpuAvgMs
				if m.StreamNetworkAvgMs < 0 {
					m.StreamNetworkAvgMs = 0
				}
			}
			if age, ok := remoteSource.Age(); ok {
				m.StreamAgeMs = float64(age) / ms
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
