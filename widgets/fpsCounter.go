package widgets

import (
	"strconv"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"
)

type fpsState struct {
	fps        float64
	frameCount int
	lastTime   time.Time
}

type FpsCounterStyle struct {
	*fpsState

	UpdateInterval time.Duration

	Direction layout.Direction
	Padding   int

	LabelStyle
}

func FpsCounter(freq int, direction layout.Direction, size unit.Sp, pad int) (f FpsCounterStyle) {
	f.fpsState = &fpsState{}
	f.UpdateInterval = time.Second / time.Duration(freq)
	f.Direction = direction
	f.Padding = pad
	f.LabelStyle = Label(size, "0")
	return f
}

func (f FpsCounterStyle) update() {
	f.frameCount++
	now := time.Now()
	elapsed := now.Sub(f.lastTime)

	if elapsed >= f.UpdateInterval {
		f.fps = float64(f.frameCount) / elapsed.Seconds()
		f.frameCount = 0
		f.lastTime = now
	}
}

func (f FpsCounterStyle) Layout(gtx layout.Context) layout.Dimensions {
	f.update()
	return f.Direction.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return Label(f.TextSize, strconv.FormatInt(int64(f.fps), 10)).Layout(gtx)
	})
}
