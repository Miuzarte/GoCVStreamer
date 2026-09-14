package ui

import (
	"context"
	"fmt"
	"image"
	"sync/atomic"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/Miuzarte/GoCVStreamer/logger"
	"github.com/Miuzarte/GoCVStreamer/widgets"
)

var log = logger.New("UI")

type Config struct {
	Title     string
	MinSize   image.Point
	Size      image.Point
	Shortcuts widgets.Shortcuts

	// OnHWND 在窗口句柄可用时回调一次 (hwnd == 0 表示句柄已失效)。
	//
	// 必须在拿到句柄后立刻做的操作放这里, 例如 SetWindowDisplayAffinity ——
	// 那个 API 只对**本进程**的窗口有效, 所以句柄只能来自这里, 不能靠
	// GetForegroundWindow 去猜 (猜错时不但设不上, 还会让功能变成"只有在窗口
	// 恰好处于前台时才有效")。
	OnHWND func(hwnd uintptr)
}

type Window struct {
	app         app.Window
	cfg         Config
	drawers     []Drawer
	screenImg   *image.RGBA
	bounds      image.Point
	drawEnabled bool
	drawScale   DScale

	// hidden 为 true 时暂停渲染 (失焦或被最小化)。
	//
	// 状态来自 app.ConfigEvent: Windows 后端在 WM_SETFOCUS / WM_KILLFOCUS 上
	// 改 config.Focused 并立刻发一次 ConfigEvent, 最小化/最大化走 GetWindowPlacement
	// 那条路。所以是推送式通知, 不需要轮询, 也不需要自己去拿窗口句柄。
	//
	// 初始为 false (正常渲染): 免得窗口还没收到第一个 ConfigEvent 就被误判成后台,
	// 结果一启动就是空白。
	hidden atomic.Bool

	// hwnd 是本窗口的句柄, 由 Gio 的视图事件送达 (见 hwnd_windows.go)
	hwnd atomic.Uintptr
}

func NewWindow(cfg Config) *Window {
	if cfg.Title == "" {
		cfg.Title = "GoCVStreamer"
	}
	if cfg.MinSize.X == 0 {
		cfg.MinSize = image.Pt(1280, 720)
	}
	if cfg.Size.X == 0 {
		cfg.Size = image.Pt(1280, 720)
	}

	w := &Window{
		cfg:         cfg,
		drawEnabled: true,
	}
	w.app.Option(
		app.Title(cfg.Title),
		app.MinSize(unit.Dp(cfg.MinSize.X), unit.Dp(cfg.MinSize.Y)),
		app.Size(unit.Dp(cfg.Size.X), unit.Dp(cfg.Size.Y)),
	)
	return w
}

func (w *Window) Register(d Drawer) {
	w.drawers = append(w.drawers, d)
}

func (w *Window) SetScreenImage(img *image.RGBA) {
	w.screenImg = img
}

func (w *Window) SetBounds(bounds image.Point) {
	w.bounds = bounds
}

func (w *Window) SetDrawEnabled(v bool) {
	w.drawEnabled = v
}

func (w *Window) SetShortcuts(s widgets.Shortcuts) {
	w.cfg.Shortcuts = s
}

func (w *Window) DrawEnabled() bool {
	return w.drawEnabled
}

func (w *Window) App() *app.Window {
	return &w.app
}

// Invalidate 请求重绘。暂停渲染期间直接丢弃 —— 这是省 CPU 的第一层:
// 没有重绘请求就不会产生 FrameEvent, 也就不会有纹理上传和 Present。
func (w *Window) Invalidate() {
	if w.hidden.Load() {
		return
	}
	w.app.Invalidate()
}

// Hidden 报告窗口当前是否处于暂停渲染状态 (失焦或已最小化)。
func (w *Window) Hidden() bool {
	return w.hidden.Load()
}

// HWND 返回本窗口的句柄; 尚未创建或已失效时返回 0。
func (w *Window) HWND() uintptr {
	return w.hwnd.Load()
}

// setHWND 记录句柄并通知 OnHWND 回调。句柄没变化时不重复回调。
func (w *Window) setHWND(hwnd uintptr) {
	if w.hwnd.Swap(hwnd) == hwnd {
		return
	}
	log.Info().Uint64("hwnd", uint64(hwnd)).Msg("window handle changed")
	if w.cfg.OnHWND != nil {
		w.cfg.OnHWND(hwnd)
	}
}

// applyConfig 由 app.ConfigEvent 驱动, 维护暂停状态。
func (w *Window) applyConfig(c app.Config) {
	hidden := !c.Focused || c.Mode == app.Minimized
	was := w.hidden.Swap(hidden)
	if was == hidden {
		return
	}
	log.Info().
		Bool("focused", c.Focused).
		Str("mode", c.Mode.String()).
		Bool("hidden", hidden).
		Msg("ui render pause toggled")
	if was && !hidden {
		// 回到前台。暂停期间一个 FrameEvent 都没产生, 窗口还停在上次呈现的那一帧,
		// 所以这里必须主动补一次, 否则要等到下一次采集回调才会刷新。
		w.app.Invalidate()
	}
}

func (w *Window) Run(ctx context.Context) {
	var ops op.Ops
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		switch e := w.app.Event().(type) {
		case app.DestroyEvent:
			if e.Err != nil {
				log.Error().
					Err(e.Err).
					Msg("window error")
			} else {
				log.Debug().
					Msg("window closed normally")
			}
			return

		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)

			// 暂停渲染的第二层: 连绘制指令都不建。
			//
			// 第一层 (Invalidate 被丢弃) 挡住了我们自己发起的重绘, 但系统仍可能因为
			// 窗口尺寸变化 / WM_PAINT / 恢复窗口而直接产生一帧。这一层保证那种情况下
			// 也不去碰那一整张屏幕纹理 (2560x1440 RGBA, 约 14.7MB 的纹理上传)。
			//
			// FrameEvent 必须应答, 否则 Gio 的帧状态机不会进入下一轮, 所以这里仍然
			// 调一次 e.Frame, 只是 ops 是空的 (等价于清屏后呈现一帧)。
			if w.hidden.Load() {
				e.Frame(gtx.Ops)
				continue
			}

			err := w.cfg.Shortcuts.Match(gtx)
			if err != nil {
			}

			if w.screenImg != nil && w.bounds.X > 0 && w.bounds.Y > 0 {
				w.drawScale = NewDScale(w.bounds, gtx.Constraints.Max)
				w.drawScreen(gtx)
			}

			if w.drawEnabled {
				for _, d := range w.drawers {
					d.Draw(gtx, w.drawScale)
				}
			}

			e.Frame(gtx.Ops)

		case app.ConfigEvent:
			w.applyConfig(e.Config)
		default:
			// 视图事件带着本窗口的句柄 (Windows 上是 app.Win32ViewEvent),
			// 不归上面的类型分支管, 单独在这里取出来转给 OnHWND。
			if hwnd, ok := viewHWND(e); ok {
				w.setHWND(hwnd)
				continue
			}
			log.Trace().
				Str("eventType", fmt.Sprintf("%T", e)).
				Any("event", e).
				Msg("window event")
		}
	}
}

func (w *Window) drawScreen(gtx layout.Context) {
	gtxBounds := gtx.Constraints.Max
	gtxW, gtxH := gtxBounds.X, gtxBounds.Y

	imgBounds := w.screenImg.Bounds()
	imgW, imgH := imgBounds.Dx(), imgBounds.Dy()

	scale := min(float32(gtxW)/float32(imgW), float32(gtxH)/float32(imgH))

	// 实际绘制大小
	drawW := int(float32(imgW) * scale)
	drawH := int(float32(imgH) * scale)

	// 居中
	defer op.Offset(image.Pt((gtxW-drawW)/2, (gtxH-drawH)/2)).Push(gtx.Ops).Pop()
	// 缩放
	defer op.Affine(f32.AffineId().Scale(f32.Pt(0, 0), f32.Pt(scale, scale))).Push(gtx.Ops).Pop()

	// 绘制
	paint.NewImageOp(w.screenImg).Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
}
