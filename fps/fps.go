package fps

import (
	"time"
)

type state struct {
	fps        float64
	frametime  time.Duration
	frameCount int
	lastCount  time.Time
	lastUpdate time.Time
}

type Counter struct {
	*state
	UpdateInterval time.Duration
}

func NewCounter(updateInterval time.Duration) Counter {
	return Counter{state: &state{}, UpdateInterval: updateInterval}
}

func (fc *Counter) update() {
	fc.frameCount++
	now := time.Now()

	elapsed := now.Sub(fc.lastUpdate)
	if elapsed >= fc.UpdateInterval {
		fc.fps = float64(fc.frameCount) / elapsed.Seconds()
		fc.frametime = now.Sub(fc.lastCount)
		fc.frameCount = 0
		fc.lastUpdate = now
	}

	fc.lastCount = now
}

func (fc *Counter) Count() (fps float64, frametime time.Duration) {
	fc.update()
	return fc.fps, fc.frametime
}
