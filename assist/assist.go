package assist

import (
	"context"
	"fmt"
	"image"
	"math"
	"strings"
	"sync"
	"sync/atomic"
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
	RequireKeys      []keystate.KeyCode
	RequireMouseMove bool

	// MaxAge 为结果寿命上限 (从结果发布 / 收到时刻算起), 超过则本 tick 不再注入 (0 = 不限制)
	//
	// 一个结果最多驱动 MaxAge 时长的注入; 检测被游戏抢占而变慢时 (30FPS -> 15FPS),
	// 旧结果不会继续被反复注入, 也就不会把鼠标一直拉向过旧的位置
	// 一般取 1 / 目标检测 FPS: 30FPS 下结果每 33ms 更新一次, 正常情况几乎不会被门控
	MaxAge time.Duration
}

func DefaultConfig() Config {
	return Config{
		Enabled:          true,
		Horizontal:       true,
		Vertical:         false,
		Speed:            8.0,
		InnerRatio:       0.5,
		RequireKeys:      []keystate.KeyCode{keystate.VK_RBUTTON},
		RequireMouseMove: true,
	}
}

type Engine struct {
	cfg Config
	mu  sync.RWMutex

	bounds  image.Rectangle
	sources []detector.Source
	keys    *keystate.Tracker
	mover   mouse.Mover

	foregroundAllowed func() bool

	targetBox image.Rectangle
	isActive  bool

	// now 注入时钟, 便于单测; 默认 time.Now
	now func() time.Time
	// lastAge 为最近一次采纳结果的帧龄
	lastAge time.Duration
	// gatedCount 为因帧龄超限而放弃注入的 tick 数
	gatedCount atomic.Uint64
}

func New(cfg Config, sources []detector.Source, bounds image.Rectangle, mover mouse.Mover) *Engine {
	if mover == nil {
		mover = mouse.LocalMover{}
	}
	return &Engine{
		cfg:     cfg,
		bounds:  bounds,
		sources: sources,
		keys:    keystate.NewTracker(),
		mover:   mover,
		now:     time.Now,
	}
}

// SetForegroundAllowed 设置前台门控: 返回 false 时 assist 完全停用 (如非目标游戏前台)
func (e *Engine) SetForegroundAllowed(fn func() bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.foregroundAllowed = fn
}

func (e *Engine) DisplayState(sb *strings.Builder) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.cfg.Enabled {
		sb.WriteString("| Aim:OFF |")
		return
	}
	if e.foregroundAllowed != nil && !e.foregroundAllowed() {
		sb.WriteString("| Aim:OFF(FG) |")
		return
	}

	mode := "H"
	if e.cfg.Vertical {
		mode += "V"
	}
	fmt.Fprintf(sb, "| Aim:%s V:%.1f Age:%.0fms", mode, e.cfg.Speed, float64(e.lastAge)/float64(time.Millisecond))
	if e.cfg.MaxAge > 0 && e.lastAge > e.cfg.MaxAge {
		fmt.Fprintf(sb, " STALE(gated:%d)", e.gatedCount.Load())
	}
	sb.WriteString(" |")
}

var lastDoubleAlt = time.Now()

func (e *Engine) ProcessKeys() {
	e.mu.Lock()
	defer e.mu.Unlock()

	alt := keystate.IsDown(keystate.VK_MENU)
	lAlt := keystate.IsDown(keystate.VK_LMENU)
	rAlt := keystate.IsDown(keystate.VK_RMENU)

	if lAlt && rAlt && time.Since(lastDoubleAlt) > time.Second {
		lastDoubleAlt = time.Now()
		log.Info().
			Float64("AimAssistSpeed", e.cfg.Speed).
			Bool("AimAssistHorizontal", e.cfg.Horizontal).
			Bool("AimAssistVertical", e.cfg.Vertical).
			Send()
	}

	if alt {
		if e.keys.Pressed(keystate.VK_DELETE) {
			e.cfg.Enabled = !e.cfg.Enabled
			log.Info().
				Bool("AimAssist", e.cfg.Enabled).
				Send()
		}

		if e.cfg.Enabled && e.keys.Pressed(keystate.VK_PAUSE) {
			if e.cfg.Horizontal && !e.cfg.Vertical {
				e.cfg.Vertical = true
			} else if e.cfg.Vertical {
				e.cfg.Vertical = false
				e.cfg.Horizontal = false
			} else {
				e.cfg.Horizontal = true
			}
			log.Info().
				Bool("AimAssistHorizontal", e.cfg.Horizontal).
				Bool("AimAssistVertical", e.cfg.Vertical).
				Send()
		}

		if e.keys.Pressed(keystate.VK_PRIOR) {
			e.cfg.Speed = math.Min(20.0, e.cfg.Speed+1)
			log.Info().
				Float64("AimAssistSpeed", e.cfg.Speed).
				Send()
		}
		if e.keys.Pressed(keystate.VK_NEXT) {
			e.cfg.Speed = math.Max(1, e.cfg.Speed-1)
			log.Info().
				Float64("AimAssistSpeed", e.cfg.Speed).
				Send()
		}
	}
}

func (e *Engine) Tick() {
	e.mu.RLock()
	cfg := e.cfg
	foregroundAllowed := e.foregroundAllowed
	e.mu.RUnlock()

	// 前台门控: 非目标游戏在前台时完全停用
	if foregroundAllowed != nil && !foregroundAllowed() {
		e.mu.Lock()
		e.isActive = false
		e.targetBox = image.Rectangle{}
		e.mu.Unlock()
		return
	}

	if !cfg.Enabled {
		e.mu.Lock()
		e.isActive = false
		e.targetBox = image.Rectangle{}
		e.mu.Unlock()
		return
	}

	if len(cfg.RequireKeys) > 0 {
		anyDown := false
		for _, key := range cfg.RequireKeys {
			if keystate.IsDown(key) {
				anyDown = true
				break
			}
		}
		if !anyDown {
			e.mu.Lock()
			e.isActive = false
			e.targetBox = image.Rectangle{}
			e.mu.Unlock()
			return
		}
	}

	if cfg.RequireMouseMove && !mouse.IsMoving(50*time.Millisecond) {
		e.mu.Lock()
		e.isActive = false
		e.targetBox = image.Rectangle{}
		e.mu.Unlock()
		return
	}

	// 多来源策略: 取信息年龄最小且新鲜的来源
	// 不用推理耗时比较, 远端推理可能很快但链路本身很旧
	var results []detector.Result
	var bestInfoAge time.Duration
	var bestLatency time.Duration
	bestSet := false
	for _, src := range e.sources {
		srcResults, latency, fresh := src.Snapshot()
		if !fresh || len(srcResults) == 0 {
			continue
		}
		age := e.infoAge(srcResults[0], latency)
		if !bestSet || age < bestInfoAge {
			results = srcResults
			bestInfoAge = age
			bestLatency = latency
			bestSet = true
		}
	}
	if !bestSet {
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

	// 结果寿命 (从发布算起), 门控依据
	age := e.resultAge(target, bestLatency)

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
		e.lastAge = age
		e.isActive = false
		e.mu.Unlock()
		return
	}

	// 结果寿命超限: 继续注入就是把鼠标拉向过旧的位置, 只保留绘制
	if cfg.MaxAge > 0 && age > cfg.MaxAge {
		e.mu.Lock()
		e.targetBox = target.Box
		e.lastAge = age
		e.isActive = false
		e.mu.Unlock()
		e.gatedCount.Add(1)
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
	e.lastAge = age
	e.isActive = dx != 0 || dy != 0
	e.mu.Unlock()

	if dx != 0 || dy != 0 {
		e.mover.MoveAndMark(dx, dy)
	}
}

// infoAge 是信息年龄: 帧采集到现在, 用于在多个源里挑信息最新的一批结果
func (e *Engine) infoAge(r detector.Result, fallback time.Duration) time.Duration {
	if r.At.IsZero() {
		return fallback
	}
	return e.now().Sub(r.At)
}

// resultAge 是结果寿命: 结果发布 / 收到到现在, 门控依据
//
// 一个结果最多驱动 MaxAge 时长的注入; 检测被游戏抢占变慢时 (30FPS -> 15FPS),
// 旧结果不会继续被反复注入, 也就不会把鼠标一直拉向过旧的位置
func (e *Engine) resultAge(r detector.Result, fallback time.Duration) time.Duration {
	if r.PublishedAt.IsZero() {
		return fallback
	}
	return e.now().Sub(r.PublishedAt)
}

// Status 返回最近一次采纳结果的结果寿命与因超限被放弃的 tick 数 (供 metrics 使用)
func (e *Engine) Status() (age time.Duration, gated uint64) {
	e.mu.RLock()
	age = e.lastAge
	e.mu.RUnlock()
	return age, e.gatedCount.Load()
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
	age := e.lastAge
	cfg := e.cfg
	e.mu.RUnlock()

	if !cfg.Enabled || box.Empty() {
		return
	}

	// 颜色: 灰 = 帧龄超限已被丢弃, 红 = 正在注入, 黄 = 跟随但未注入
	color := ui.ColorYellow.NRGBA()
	if active {
		color = ui.ColorRed.NRGBA()
	}
	if cfg.MaxAge > 0 && age > cfg.MaxAge {
		color = ui.ColorGray.NRGBA()
	}

	cx := int(float64(e.bounds.Dx()) / 2)
	cy := int(float64(e.bounds.Dy()) / 2)
	center := s.Pos(image.Pt(cx, cy))

	closestX := min(max(cx, box.Min.X), box.Max.X)
	closestY := min(max(cy, box.Min.Y), box.Max.Y)
	closest := s.Pos(image.Pt(closestX, closestY))

	ui.DrawLine(gtx, color, center, closest)
}
