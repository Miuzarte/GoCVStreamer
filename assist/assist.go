package assist

import (
	"context"
	"fmt"
	"image"
	"math"
	"strings"
	"sync"
	"time"

	"gioui.org/layout"

	"github.com/Miuzarte/GoCVStreamer/detector"
	"github.com/Miuzarte/GoCVStreamer/keystate"
	"github.com/Miuzarte/GoCVStreamer/logger"
	"github.com/Miuzarte/GoCVStreamer/mouse"
	"github.com/Miuzarte/GoCVStreamer/ui"
)

var log = logger.New("Assist")

type Config struct {
	Enabled          bool
	Horizontal       bool
	Vertical         bool
	Speed            float64
	InnerRatio       float64
	RequireRButton   bool
	RequireMouseMove bool
}

func DefaultConfig() Config {
	return Config{
		Enabled:          true,
		Horizontal:       true,
		Vertical:         false,
		Speed:            4.0,
		InnerRatio:       0.5,
		RequireRButton:   true,
		RequireMouseMove: true,
	}
}

type Engine struct {
	cfg Config
	mu  sync.RWMutex

	bounds   image.Rectangle
	detector *detector.Engine
	keys     *keystate.Tracker

	targetBox image.Rectangle
	isActive  bool
}

func New(cfg Config, det *detector.Engine, bounds image.Rectangle) *Engine {
	return &Engine{
		cfg:      cfg,
		bounds:   bounds,
		detector: det,
		keys:     keystate.NewTracker(),
	}
}

func (e *Engine) Speed() float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg.Speed
}

func (e *Engine) Enabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg.Enabled
}

func (e *Engine) Horizontal() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg.Horizontal
}

func (e *Engine) Vertical() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg.Vertical
}

func (e *Engine) SetEnabled(v bool) {
	e.mu.Lock()
	e.cfg.Enabled = v
	e.mu.Unlock()
	log.Info().Bool("enabled", v).Msg("aim assist toggled")
}

func (e *Engine) SetSpeed(v float64) {
	e.mu.Lock()
	e.cfg.Speed = math.Max(0.5, v)
	e.mu.Unlock()
	log.Info().Float64("speed", e.cfg.Speed).Msg("aim assist speed changed")
}

func (e *Engine) SetHorizontal(v bool) {
	e.mu.Lock()
	e.cfg.Horizontal = v
	e.mu.Unlock()
	log.Info().Bool("horizontal", v).Msg("aim assist mode changed")
}

func (e *Engine) SetVertical(v bool) {
	e.mu.Lock()
	e.cfg.Vertical = v
	e.mu.Unlock()
	log.Info().Bool("vertical", v).Msg("aim assist mode changed")
}

func (e *Engine) DisplayState(sb *strings.Builder) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.cfg.Enabled {
		sb.WriteString("| Aim:OFF |")
		return
	}

	mode := "H"
	if e.cfg.Vertical {
		mode += "V"
	}
	fmt.Fprintf(sb, "| Aim:%s V:%.1f |", mode, e.cfg.Speed)
}

func (e *Engine) ProcessKeys() {
	e.mu.Lock()
	defer e.mu.Unlock()

	shift := keystate.IsDown(keystate.VK_SHIFT)

	if e.keys.Pressed(keystate.VK_DELETE) {
		e.cfg.Enabled = !e.cfg.Enabled
		log.Info().
			Bool("enabled", e.cfg.Enabled).
			Msg("aim assist toggled")
	}

	if e.cfg.Enabled && shift && e.keys.Pressed(keystate.VK_DELETE) {
		if e.cfg.Horizontal && !e.cfg.Vertical {
			e.cfg.Vertical = true
		} else if e.cfg.Vertical {
			e.cfg.Vertical = false
			e.cfg.Horizontal = false
		} else {
			e.cfg.Horizontal = true
		}
		log.Info().
			Bool("horizontal", e.cfg.Horizontal).
			Bool("vertical", e.cfg.Vertical).
			Msg("aim assist mode changed")
	}

	if e.keys.Pressed(keystate.VK_OEM_4) && shift {
		e.cfg.Speed = math.Max(1, e.cfg.Speed-1)
		log.Info().
			Float64("speed", e.cfg.Speed).
			Msg("aim assist speed decreased")
	}
	if e.keys.Pressed(keystate.VK_OEM_6) && shift {
		e.cfg.Speed = math.Min(20.0, e.cfg.Speed+1)
		log.Info().
			Float64("speed", e.cfg.Speed).
			Msg("aim assist speed increased")
	}
}

func (e *Engine) Tick() {
	e.mu.RLock()
	cfg := e.cfg
	e.mu.RUnlock()

	if !cfg.Enabled {
		e.mu.Lock()
		e.isActive = false
		e.targetBox = image.Rectangle{}
		e.mu.Unlock()
		return
	}

	if cfg.RequireRButton && !keystate.IsDown(keystate.VK_RBUTTON) {
		e.mu.Lock()
		e.isActive = false
		e.targetBox = image.Rectangle{}
		e.mu.Unlock()
		return
	}

	if cfg.RequireMouseMove && !mouse.IsMoving(50*time.Millisecond) {
		e.mu.Lock()
		e.isActive = false
		e.targetBox = image.Rectangle{}
		e.mu.Unlock()
		return
	}

	results, _ := e.detector.Snapshot()
	if len(results) == 0 {
		e.mu.Lock()
		e.isActive = false
		e.targetBox = image.Rectangle{}
		e.mu.Unlock()
		return
	}

	cx := float64(e.bounds.Dx()) / 2
	cy := float64(e.bounds.Dy()) / 2

	bestDist := math.MaxFloat64
	bestIdx := -1
	for i, r := range results {
		if r.Score < 0.45 {
			continue
		}
		bcx := float64(r.Box.Min.X+r.Box.Max.X) / 2
		bcy := float64(r.Box.Min.Y+r.Box.Max.Y) / 2
		dist := (bcx-cx)*(bcx-cx) + (bcy-cy)*(bcy-cy)
		if dist < bestDist {
			bestDist = dist
			bestIdx = i
		}
	}

	if bestIdx < 0 {
		e.mu.Lock()
		e.isActive = false
		e.targetBox = image.Rectangle{}
		e.mu.Unlock()
		return
	}

	target := results[bestIdx]
	bw := target.Box.Dx()
	bh := target.Box.Dy()

	innerMargin := (1.0 - cfg.InnerRatio) / 2

	stopL := float64(target.Box.Min.X) + float64(bw)*innerMargin
	stopR := float64(target.Box.Min.X) + float64(bw)*(1-innerMargin)
	stopT := float64(target.Box.Min.Y) + float64(bh)*innerMargin
	stopB := float64(target.Box.Min.Y) + float64(bh)*(1-innerMargin)

	var nearestDist float64 = math.MaxFloat64
	isOutside := false

	if cx < float64(target.Box.Min.X) {
		nearestDist = math.Min(nearestDist, float64(target.Box.Min.X)-cx)
		isOutside = true
	}
	if cx > float64(target.Box.Max.X) {
		nearestDist = math.Min(nearestDist, cx-float64(target.Box.Max.X))
		isOutside = true
	}
	if cy < float64(target.Box.Min.Y) {
		nearestDist = math.Min(nearestDist, float64(target.Box.Min.Y)-cy)
		isOutside = true
	}
	if cy > float64(target.Box.Max.Y) {
		nearestDist = math.Min(nearestDist, cy-float64(target.Box.Max.Y))
		isOutside = true
	}

	if isOutside && nearestDist >= float64(bw)/2 {
		e.mu.Lock()
		e.targetBox = target.Box
		e.isActive = false
		e.mu.Unlock()
		return
	}

	var dx, dy int

	if cfg.Horizontal {
		if cx < stopL {
			dx = int(math.Min(stopL-cx, cfg.Speed))
		} else if cx > stopR {
			dx = -int(math.Min(cx-stopR, cfg.Speed))
		}
	}

	if cfg.Vertical {
		if cy < stopT {
			dy = int(math.Min(stopT-cy, cfg.Speed))
		} else if cy > stopB {
			dy = -int(math.Min(cy-stopB, cfg.Speed))
		}
	}

	e.mu.Lock()
	e.targetBox = target.Box
	e.isActive = dx != 0 || dy != 0
	e.mu.Unlock()

	if dx != 0 || dy != 0 {
		mouse.MoveAndMark(dx, dy)
	}
}

func (e *Engine) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second / 125)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.ProcessKeys()
			e.Tick()
		}
	}
}

func (e *Engine) Draw(gtx layout.Context, s ui.DScale) {
	e.mu.RLock()
	box := e.targetBox
	active := e.isActive
	cfg := e.cfg
	e.mu.RUnlock()

	if !cfg.Enabled || box.Empty() {
		return
	}

	color := ui.ColorRed.NRGBA()
	if !active {
		color = ui.ColorYellow.NRGBA()
	}

	cx := int(float64(e.bounds.Dx()) / 2)
	cy := int(float64(e.bounds.Dy()) / 2)
	center := s.Pos(image.Pt(cx, cy))

	closestX := min(max(cx, box.Min.X), box.Max.X)
	closestY := min(max(cy, box.Min.Y), box.Max.Y)
	closest := s.Pos(image.Pt(closestX, closestY))

	ui.DrawLine(gtx, color, center, closest)
}
