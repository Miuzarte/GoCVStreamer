package main

import (
	"time"
)

type Metrics struct {
	FpsCount      float64 `json:"fps"`
	FrametimeMs   float64 `json:"frame_ms"`
	CaptureCostMs float64 `json:"capture_ms"`
	FramesElapsed int     `json:"frames_elapsed"`
	Debugging     bool    `json:"debugging"`

	Cpu float64 `json:"cpu"`

	NumGc        int     `json:"gc_count"`
	PauseAvgUs   float64 `json:"gc_pause_avg_us"`
	SinceLastGcS float64 `json:"gc_since_last_s"`

	WeaponsMatchingCostTotalMs float64 `json:"match_cost_total_ms"`
	WeaponsMatched             int     `json:"match_count"`
	WeaponsMatchingCostAvgMs   float64 `json:"match_cost_avg_ms"`

	CurrentWeapon string  `json:"current_weapon"`
	WeaponFound   bool    `json:"weapon_found"`
	WeaponVal     float32 `json:"weapon_val"`
	Idle          bool    `json:"idle"`
	Narrowing     bool    `json:"narrowing"`
}

func SnapshotMetrics() Metrics {
	weaponsMu.RLock()
	defer weaponsMu.RUnlock()

	const ms = float64(time.Millisecond)
	const us = float64(time.Microsecond)

	var m Metrics

	m.FpsCount = fpsCount
	m.FrametimeMs = float64(fpsFrametime) / ms
	m.CaptureCostMs = float64(captureCost) / ms
	m.FramesElapsed = capturer.FramesElapsed
	m.Debugging = debugging

	m.Cpu = cpu

	if lastGCStats.NumGC > 0 {
		m.NumGc = int(lastGCStats.NumGC)
		m.PauseAvgUs = float64(lastGCStats.PauseTotal) / float64(lastGCStats.NumGC) / us
		m.SinceLastGcS = time.Since(lastGCStats.LastGC).Seconds()
	}

	m.WeaponsMatchingCostTotalMs = float64(weaponsMatchingCost) / ms
	m.WeaponsMatched = weaponsMatched
	if weaponsMatched > 0 {
		m.WeaponsMatchingCostAvgMs = float64(weaponsMatchingCost) / float64(weaponsMatched) / ms
	}

	if weaponFound && weaponIndex >= 0 && weaponIndex < len(weapons) {
		m.CurrentWeapon = weapons[weaponIndex].String()
		m.WeaponFound = true
		m.WeaponVal = weapons[weaponIndex].Template.MaxVal
	}

	m.Idle = inIdle
	m.Narrowing = narrowing

	return m
}
