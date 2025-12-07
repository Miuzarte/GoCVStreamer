package fps

import (
	"time"
)

type state struct {
	fps        float64
	frameCount int
	lastTime   time.Time
}

type counter struct {
	*state
	UpdateInterval time.Duration
}

func NewCounter(updateInterval time.Duration) counter {
	return counter{state: &state{}, UpdateInterval: updateInterval}
}

func (fc *counter) update() {
	fc.frameCount++
	now := time.Now()
	elapsed := now.Sub(fc.lastTime)

	if elapsed >= fc.UpdateInterval {
		fc.fps = float64(fc.frameCount) / elapsed.Seconds()
		fc.frameCount = 0
		fc.lastTime = now
	}
}

func (fc *counter) Count() float64 {
	fc.update()
	return fc.fps
}
