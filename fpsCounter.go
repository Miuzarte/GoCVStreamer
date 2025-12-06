package main

import (
	"time"
)

type fpsState struct {
	fps        float64
	frameCount int
	lastTime   time.Time
}

type FpsCounter struct {
	*fpsState
	UpdateInterval time.Duration
}

func NewFpsCounter(updateInterval time.Duration) FpsCounter {
	return FpsCounter{fpsState: &fpsState{}, UpdateInterval: updateInterval}
}

func (fc *FpsCounter) update() {
	fc.frameCount++
	now := time.Now()
	elapsed := now.Sub(fc.lastTime)

	if elapsed >= fc.UpdateInterval {
		fc.fps = float64(fc.frameCount) / elapsed.Seconds()
		fc.frameCount = 0
		fc.lastTime = now
	}
}

func (fc *FpsCounter) Count() float64 {
	fc.update()
	return fc.fps
}
