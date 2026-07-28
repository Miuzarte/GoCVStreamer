package ui

import (
	"context"
	"fmt"
	"image"

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
}

type Window struct {
	app         app.Window
	cfg         Config
	drawers     []Drawer
	screenImg   *image.RGBA
	bounds      image.Point
	drawEnabled bool
	drawScale   DScale
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

func (w *Window) Invalidate() {
	w.app.Invalidate()
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
		default:
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
