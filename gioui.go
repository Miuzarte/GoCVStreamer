package main

import (
	"context"
	"fmt"
	"image"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget/material"
	"github.com/Miuzarte/GoCVStreamer/widgets"
	"golang.org/x/sys/windows"
)

var (
	window     app.Window
	gioDisplay image.Image
)

var (
	dScale unit.Metric
	mTheme = material.NewTheme()
	gioFps widgets.FpsCounterStyle

	shortcuts = widgets.NewShortcuts(&window,
		widgets.Shortcut{
			Key: widgets.NewShortcut(0, 0, key.NameSpace),
			F: func() {
				for i, tmpl := range weapons {
					fmt.Printf("[%d] %s %.2f%%\n", i, tmpl.Name, tmpl.Template.MaxVal*100)
				}
			},
		},
		widgets.Shortcut{
			Key: widgets.NewShortcut(0, 0, "P", "p"),
			F: func() {
				windowHandel = windows.GetForegroundWindow()
				log.Infof("window handel: %#X", windowHandel)
			},
		},
		widgets.Shortcut{
			Key: widgets.NewShortcut(0, 0, "R", "r"),
			F: func() {
				capturer.FramesElapsed = 0
				log.Info("capturer.FramesElapsed reset")
			},
		},
	)
)

const FPS_TEXT_SIZE unit.Sp = 32

func init() {
	widgets.Theme = mTheme
	gioFps = widgets.FpsCounter(10, layout.SE, FPS_TEXT_SIZE, 0)
}

func runGioui(ctx context.Context) {
	window.Option(
		app.Title(windowTitle),
		app.MinSize(1280, 720),
		app.Size(1280, 720),
	)

	go func() {
		var ops op.Ops
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			switch e := window.Event().(type) {
			case app.DestroyEvent:
				if e.Err != nil {
					log.Errorf("window error: %v", e.Err)
				}
				return

			case app.FrameEvent:
				gtx := app.NewContext(&ops, e)
				dScale = gtx.Metric

				err := shortcuts.Match(gtx)
				if err != nil {
					log.Warnf("shortcuts match error: %v", err)
				}

				gioLayoutImage(gtx, gioDisplay)

				e.Frame(gtx.Ops)

			case app.ConfigEvent:
			// case app.wakeupEvent:
			default:
				log.Tracef("event[%T]: %v", e, e)
			}
		}
	}()

	app.Main()
}

func gioLayoutImage(gtx layout.Context, img image.Image) layout.Dimensions {
	if img == nil { // not ready
		return layout.Dimensions{}
	}
	const GIO_INFINITY = 1000000

	ctxW := gtx.Constraints.Max.X
	ctxH := gtx.Constraints.Max.Y

	bounds := img.Bounds()
	imgW := bounds.Dx()
	imgH := bounds.Dy()

	// 检查是否为 list layout
	isHorizontalList := ctxW == GIO_INFINITY
	isVerticalList := ctxH == GIO_INFINITY

	var scale float32
	if isHorizontalList && !isVerticalList {
		// 横向 list，仅受高度约束
		scale = float32(ctxH) / float32(imgH)
	} else if isVerticalList && !isHorizontalList {
		// 纵向 list，仅受宽度约束
		scale = float32(ctxW) / float32(imgW)
	} else if isHorizontalList && isVerticalList {
		// 都无限制，原图大小
		scale = 1
	} else {
		// 正常情况，按容器适应
		scaleW := float32(ctxW) / float32(imgW)
		scaleH := float32(ctxH) / float32(imgH)
		scale = min(scaleW, scaleH)
	}

	// 实际绘制大小
	drawW := int(float32(imgW) * scale)
	drawH := int(float32(imgH) * scale)

	// 居中
	if !isHorizontalList && !isVerticalList {
		offsetStack := op.Offset(image.Pt((ctxW-drawW)/2, (ctxH-drawH)/2)).Push(gtx.Ops)
		defer offsetStack.Pop()
	}
	// 缩放
	affineStack := op.Affine(f32.Affine2D{}.Scale(f32.Pt(0, 0), f32.Pt(scale, scale))).Push(gtx.Ops)
	defer affineStack.Pop()

	// 绘制
	paint.NewImageOp(img).Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops) // ?

	// 返回实际显示尺寸，list方向用图片实际长宽替换
	size := image.Point{ctxW, ctxH}
	if isHorizontalList {
		size.X = drawW
	}
	if isVerticalList {
		size.Y = drawH
	}

	return layout.Dimensions{Size: size}
}
