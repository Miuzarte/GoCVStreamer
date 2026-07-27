package matcher

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"runtime"
	"sync"
	"time"

	"gioui.org/layout"

	"github.com/Miuzarte/GoCVStreamer/capturer"
	"github.com/Miuzarte/GoCVStreamer/fps"
	"github.com/Miuzarte/GoCVStreamer/ui"
	w "github.com/Miuzarte/GoCVStreamer/weapon"
	ws "github.com/Miuzarte/GoCVStreamer/weapons"
	"gocv.io/x/gocv"
)

const (
	MatchThreshold = 0.9

	WEAPON_INDEX_NONE = -1

	DRAW_NEGATIVE_RESULT = false
)

type Config struct {
	CapSrv    *capturer.Server
	ROIRect   image.Rectangle
	Weapons   ws.Weapons
	WeaponsMu *sync.RWMutex
	Debugging bool
}

type MatchResult struct {
	Found       bool
	WeaponIndex int
	Confidence  float32
	Matched     int
	Box         image.Rectangle
}

type Stats struct {
	FPS        float64
	CostMs     float64
	Matched    int
	Found      bool
	Confidence float32
	Idle       bool
	Narrowing  bool
}

type Engine struct {
	cfg Config
	fp  fps.Counter

	mu             sync.RWMutex
	roiRect        image.Rectangle
	result         MatchResult
	stats          Stats
	lastFoundTime  time.Time
	inIdle         bool
	narrowing      bool
	lastSlot       w.Slot
	lastTempl      int
	showROIPosTill time.Time

	resultCh chan int
	capSrv   *capturer.Server
}

func New(cfg Config) *Engine {
	return &Engine{
		cfg:        cfg,
		fp:         fps.NewCounter(time.Second),
		roiRect:    cfg.ROIRect,
		resultCh:   make(chan int, 1),
		lastSlot:   w.Slot(w.SLOT_UNDEFINED),
		capSrv:     cfg.CapSrv,
	}
}

func (e *Engine) ResultCh() <-chan int {
	return e.resultCh
}

func (e *Engine) Result() MatchResult {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.result
}

func (e *Engine) Stats() Stats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.stats
}

func (e *Engine) SetROI(r image.Rectangle) {
	e.mu.Lock()
	e.roiRect = r
	e.showROIPosTill = time.Now().Add(time.Second * 3)
	e.mu.Unlock()
}

func (e *Engine) ROI() image.Rectangle {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.roiRect
}

func (e *Engine) InIdle() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.inIdle
}

func (e *Engine) Weapons() ws.Weapons {
	e.cfg.WeaponsMu.RLock()
	defer e.cfg.WeaponsMu.RUnlock()
	return e.cfg.Weapons
}

func (e *Engine) ReloadWeapons() {
	e.cfg.WeaponsMu.Lock()
	defer e.cfg.WeaponsMu.Unlock()
	if len(e.cfg.Weapons) != 0 {
		e.cfg.Weapons.Close()
	}
}

func (e *Engine) AddWeapon(path string, createMask bool, flag gocv.IMReadFlag) error {
	e.cfg.WeaponsMu.Lock()
	defer e.cfg.WeaponsMu.Unlock()
	return e.cfg.Weapons.Append(path, createMask, flag)
}

func (e *Engine) WeaponIndex() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.result.WeaponIndex
}

func (e *Engine) Run(ctx context.Context) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	capture := gocv.NewMat()
	defer capture.Close()

	const (
		interval         = time.Second / 5
		intervalIdle     = time.Second / 2
		dropIdleDuration = time.Second * 5
	)

	tickerNormal := time.NewTicker(interval)
	defer tickerNormal.Stop()
	tickerIdle := time.NewTicker(intervalIdle)
	defer tickerIdle.Stop()
	inner := make(chan time.Time, 2)
	defer close(inner)

	for {
		select {
		case <-ctx.Done():
			return

		case t, ok := <-tickerNormal.C:
			if !ok {
				return
			}
			if e.InIdle() {
				continue
			}
			select {
			case inner <- t:
			default:
			}
		case t, ok := <-tickerIdle.C:
			if !ok {
				return
			}
			if !e.InIdle() {
				continue
			}
			select {
			case inner <- t:
			default:
			}

		case _, ok := <-inner:
			if !ok {
				return
			}

			reqInterval := interval
			if e.InIdle() {
				reqInterval = intervalIdle
			}
			e.capSrv.Request(capturer.TagOpenCV, reqInterval)

			frameID := e.capSrv.ReadFrameID()
			if frameID == 0 {
				continue
			}

			frameMat := e.capSrv.ReadMat()
			frameMat.CopyTo(&capture)

			roi := e.ROI()
			if !roi.In(e.capSrv.Bounds()) {
				continue
			}

			captureRoi := capture.Region(roi)
			tStart := time.Now()

			slotFilter := w.Slot(w.SLOT_UNDEFINED)
			e.mu.RLock()
			if e.narrowing {
				slotFilter = e.lastSlot.Opposite()
			}
			e.mu.RUnlock()

			idx, matched, found := e.matchWeapon(captureRoi, slotFilter)
			elapsed := time.Since(tStart)
			captureRoi.Close()

			fps, _ := e.fp.Count()

			e.mu.Lock()
			e.stats.FPS = fps
			e.stats.CostMs = float64(elapsed) / float64(time.Millisecond)
			e.stats.Matched = matched

			if found {
				e.lastFoundTime = time.Now()
				e.inIdle = false
				e.narrowing = false

				e.cfg.WeaponsMu.RLock()
				var confidence float32
				if idx >= 0 && idx < len(e.cfg.Weapons) {
					confidence = e.cfg.Weapons[idx].Template.MaxVal
					e.lastSlot = e.cfg.Weapons[idx].Class.Detail().Slot
					if e.lastSlot != w.SLOT_UNDEFINED && !e.lastSlot.Is(w.SLOT_MIX) {
						e.narrowing = true
					}
					e.lastTempl = idx
				}
				e.cfg.WeaponsMu.RUnlock()

				e.result = MatchResult{Found: true, WeaponIndex: idx, Confidence: confidence, Matched: matched}
				e.stats.Found = true
				e.stats.Confidence = confidence
				e.stats.Idle = false
				e.stats.Narrowing = e.narrowing
				e.mu.Unlock()

				select {
				case e.resultCh <- idx:
				default:
				}
			} else {
				if time.Since(e.lastFoundTime) > dropIdleDuration && !e.cfg.Debugging {
					e.inIdle = true
					e.narrowing = false
					e.lastSlot = w.SLOT_UNDEFINED
				}
				e.result = MatchResult{}
				e.stats.Found = false
				e.stats.Idle = e.inIdle
				e.stats.Narrowing = e.narrowing
				e.mu.Unlock()

				select {
				case e.resultCh <- WEAPON_INDEX_NONE:
				default:
				}
			}
		}
	}
}

func (e *Engine) matchWeapon(image gocv.Mat, slotFilter w.Slot) (templateIndex, templateMatched int, found bool) {
	e.cfg.WeaponsMu.RLock()
	defer e.cfg.WeaponsMu.RUnlock()

	const method = gocv.TmCcoeffNormed

	e.mu.RLock()
	start := e.lastTempl
	e.mu.RUnlock()

	// 从上次成功的模板开始往下匹配
	for j := range e.cfg.Weapons {
		i := j + start
		i %= len(e.cfg.Weapons)
		templateIndex = i

		tmpl := e.cfg.Weapons[i]

		if slotFilter != w.SLOT_UNDEFINED && j != 0 && !tmpl.Class.Detail().Slot.Has(slotFilter) {
			continue
		}

		if err := tmpl.Template.Match(image, method); err != nil {
			panic(err)
		}

		templateMatched++

		if tmpl.Template.MaxVal >= MatchThreshold {
			// 跳过剩余匹配
			found = true
			break
		}
	}

	return
}

func (e *Engine) Draw(gtx layout.Context, s ui.DScale) {
	e.mu.RLock()
	roi := e.roiRect
	result := e.result
	showPosTill := e.showROIPosTill
	e.mu.RUnlock()

	roiRect := s.Rect(roi)
	ui.DrawBorder(gtx, ui.ColorCoral.NRGBA(), roiRect)

	labelPos := s.Pos(image.Pt(roi.Min.X, roi.Min.Y))
	labelPos.Y -= int(float64(ui.FontSize) * 1.25)
	if time.Now().Before(showPosTill) {
		ui.DrawLabel(gtx, ui.ColorCoral.NRGBA(), labelPos, ui.FontSize, fmt.Sprint(roi))
	} else {
		ui.DrawLabel(gtx, ui.ColorCoral.NRGBA(), labelPos, ui.FontSize, "ROI")
	}

	e.cfg.WeaponsMu.RLock()
	defer e.cfg.WeaponsMu.RUnlock()

	// 无可信匹配时, 黄框显示最高匹配的模板
	colorPos := ui.ColorGreen.NRGBA()
	var weaponPos *w.Weapon
	if result.Found && result.WeaponIndex >= 0 && result.WeaponIndex < len(e.cfg.Weapons) {
		weaponPos = e.cfg.Weapons[result.WeaponIndex]
	} else {
		colorPos = ui.ColorYellow.NRGBA()
		_, max := e.cfg.Weapons.MinMaxIndex()
		if max >= 0 && max < len(e.cfg.Weapons) {
			weaponPos = e.cfg.Weapons[max]
		}
	}

	if weaponPos == nil || weaponPos.Template.MaxVal < 0.5 {
		return
	}

	e.drawOpenCVResult(gtx, s, roi, roiRect, weaponPos, colorPos, 0)

	if DRAW_NEGATIVE_RESULT {
		colorNeg := ui.ColorCyan.NRGBA()
		min, _ := e.cfg.Weapons.MinMaxIndex()
		if min >= 0 && min < len(e.cfg.Weapons) {
			e.drawOpenCVResult(gtx, s, roi, roiRect, e.cfg.Weapons[min], colorNeg, weaponPos.Template.Height)
		}
	}
}

func (e *Engine) drawOpenCVResult(gtx layout.Context, s ui.DScale, roi image.Rectangle, roiRect image.Rectangle, weapon *w.Weapon, color color.NRGBA, tmplPosOffset int) {
	// 与ROI左对齐
	tmplPos := s.Pos(image.Pt(roi.Min.X, roi.Max.Y+tmplPosOffset))
	tmplPos.Y += ui.BorderThickness / 2
	// 匹配的模板本身
	ui.DrawImage(gtx, tmplPos, weapon.Template.Raw)

	box := s.Rect(image.Rect(
		weapon.Template.MaxLoc.X,
		weapon.Template.MaxLoc.Y,
		weapon.Template.MaxLoc.X+weapon.Template.Width,
		weapon.Template.MaxLoc.Y+weapon.Template.Height,
	).Add(roi.Min))
	ui.DrawBorder(gtx, color, box)

	ui.DrawTextRight(gtx, color, roiRect, 0, weapon.Name)
	ui.DrawTextRight(gtx, color, roiRect, 1, ui.FormatPct(weapon.Template.MaxVal))
}
