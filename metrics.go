package main

import (
	"time"
)

type Metrics struct {
	CaptureFps    float64 `json:"capture_fps"`
	CaptureCostMs float64 `json:"capture_cost_ms"`
	OpenCVFps     float64 `json:"opencv_fps"`
	OpenCVCostMs  float64 `json:"opencv_cost_ms"`
	YoloFps       float64 `json:"yolo_fps"`
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

	CurrentWeapon string  `json:"current_weapon"`
	WeaponFound   bool    `json:"weapon_found"`
	WeaponVal     float32 `json:"weapon_val"`
	Idle          bool    `json:"idle"`
	Narrowing     bool    `json:"narrowing"`

	PersonDetCostMs float64 `json:"person_det_ms"`
	PersonDetCount  int     `json:"person_det_count"`
}

func SnapshotMetrics() Metrics {
	weaponsMu.RLock()
	defer weaponsMu.RUnlock()

	const ms = float64(time.Millisecond)
	const us = float64(time.Microsecond)

	var m Metrics

	m.CaptureFps = captureFps
	m.CaptureCostMs = float64(captureCost) / ms
	m.OpenCVFps = opencvFps
	m.OpenCVCostMs = float64(opencvCost) / ms
	m.YoloFps = yoloFps
	m.YoloCostMs = float64(yoloCost) / ms

	m.FramesElapsed = capturer.FramesElapsed
	m.Debugging = debugging

	m.Cpu = cpu

	if lastGCStats.NumGC > 0 {
		m.GcCount = int(lastGCStats.NumGC)
		m.GcPauseAvgUs = float64(lastGCStats.PauseTotal) / float64(lastGCStats.NumGC) / us
		m.GcSinceLastS = time.Since(lastGCStats.LastGC).Seconds()
	}

	m.MatchCostTotalMs = float64(opencvCost) / ms
	m.MatchCount = weaponsMatched
	if weaponsMatched > 0 {
		m.MatchCostAvgMs = float64(opencvCost) / float64(weaponsMatched) / ms
	}

	if weaponFound && weaponIndex >= 0 && weaponIndex < len(weapons) {
		m.CurrentWeapon = weapons[weaponIndex].String()
		m.WeaponFound = true
		m.WeaponVal = weapons[weaponIndex].Template.MaxVal
	}

	m.Idle = inIdle
	m.Narrowing = narrowing

	if yoloEngine != nil {
		_, count, pcost := yoloEngine.Snapshot()
		m.PersonDetCount = count
		m.PersonDetCostMs = float64(pcost) / ms
	}

	return m
}
