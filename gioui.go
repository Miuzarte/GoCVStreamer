package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget/material"
	"github.com/Miuzarte/GoCVStreamer/fps"
	"github.com/Miuzarte/GoCVStreamer/template"
	"github.com/Miuzarte/GoCVStreamer/widgets"
	"golang.org/x/sys/windows"
)

const (
	fontSize        = 16
	borderThickness = 2
)

var (
	window     app.Window
	gioDisplay image.Image
)

var (
	dScale unit.Metric
	mTheme = material.NewTheme()

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
				log.Infof("parent process id: %d", parentProcessId)
				log.Infof("process id: %d", processId)
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

func init() {
	mTheme.Fg = color.NRGBA(colorCoral)
	mTheme.Bg = color.NRGBA(colorWhite)
	mTheme.ContrastFg = color.NRGBA(colorWhite)
	mTheme.ContrastBg = color.NRGBA(colorCoral)
	mTheme.Face = "Maple Mono Normal NF CN"
	widgets.Theme = mTheme
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

				tStart := time.Now()
				if gioDisplay != nil {
					layoutDisplay(gtx, gioDisplay)
				}
				layoutGocvInfo(gtx)
				drawCost = time.Since(tStart)

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

func layoutDisplay(gtx layout.Context, img image.Image) {
	const gioInf = 1000000

	gtxW := gtx.Constraints.Max.X
	gtxH := gtx.Constraints.Max.Y

	bounds := img.Bounds()
	imgW := bounds.Dx()
	imgH := bounds.Dy()

	scale := min(float32(gtxW)/float32(imgW), float32(gtxH)/float32(imgH))

	// 实际绘制大小
	drawW := int(float32(imgW) * scale)
	drawH := int(float32(imgH) * scale)

	// 居中
	defer op.Offset(image.Pt((gtxW-drawW)/2, (gtxH-drawH)/2)).Push(gtx.Ops).Pop()
	// 缩放
	defer op.Affine(f32.Affine2D{}.Scale(f32.Pt(0, 0), f32.Pt(scale, scale))).Push(gtx.Ops).Pop()

	// 绘制
	paint.NewImageOp(img).Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
}

var cvFps = fps.NewCounter(time.Second / 2)

func layoutGocvInfo(gtx layout.Context) {
	const ms = float64(time.Millisecond)
	status := fmt.Sprintf(
		"| FPS: %.2f | 截图: %.2fms | %d | 绘制: %.2fms |\n| 匹配: %.2fms/%d=%.2fms |",
		cvFps.Count(),
		float64(captureCost)/ms,
		capturer.FramesElapsed,
		float64(drawCost)/ms,
		float64(templatesMatchingCost)/ms, weaponMatched,
		float64(templatesMatchingCost)/float64(weaponMatched)/ms,
	)
	widgets.Label(fontSize*1.5, status).Layout(gtx)

	roiRectScaled := scaleRect(
		capturer.Bounds().Max, gtx.Constraints.Max,
		roiRect,
	)
	layoutRectAbsPos(gtx, color.NRGBA(colorCoral), roiRectScaled)

	labelPosScaled := scalePos(
		capturer.Bounds().Max, gtx.Constraints.Max,
		image.Pt(
			roiRect.Min.X,
			roiRect.Min.Y-(fontSize*2.5),
		),
	)
	layoutLabelAbsPos(gtx, color.NRGBA(colorCoral), labelPosScaled, fontSize, "ROI")

	colorPos := colorGreen
	colorNeg := colorCyan
	min, max := weapons.MinMaxIndex()
	var weaponPos *Weapon
	weaponNeg := weapons[min]
	if weaponFound {
		weaponPos = weapons[weaponIndex]
	} else {
		weaponPos = weapons[max]
		// 黄框显示最高匹配的模板
		colorPos = colorYellow
	}

	if weaponPos.Template.MaxVal > 0 {
		tmplPosPos := scalePos(
			capturer.Bounds().Max, gtx.Constraints.Max,
			image.Pt(
				roiRect.Min.X, // 与ROI左对齐
				roiRect.Max.Y,
			),
		)
		tmplPosPos.Y += borderThickness / 2
		layoutImageAbsPos(gtx, tmplPosPos, weaponPos.Template.Raw) // 匹配的模板本身
		layoutResultRect(gtx, color.NRGBA(colorPos), &weaponPos.Template)
		layoutTextRight(gtx, color.NRGBA(colorPos), roiRectScaled, 0, weaponPos.Name)
		layoutTextRight(gtx, color.NRGBA(colorPos), roiRectScaled, 1, fmt.Sprintf("%.2f%%", weaponPos.Template.MaxVal*100))

		if drawNeg {
			tmplNegPos := image.Pt(
				tmplPosPos.X,
				tmplPosPos.Y+weaponPos.Template.Height,
			)
			layoutImageAbsPos(gtx, tmplNegPos, weaponNeg.Template.Raw)
			layoutResultRect(gtx, color.NRGBA(colorNeg), &weaponNeg.Template)
			layoutTextRight(gtx, color.NRGBA(colorNeg), roiRectScaled, 3, weaponNeg.Name)
			layoutTextRight(gtx, color.NRGBA(colorNeg), roiRectScaled, 4, fmt.Sprintf("%.2f%%", weaponNeg.Template.MaxVal*100))
		}

	}
}

func layoutResultRect(gtx layout.Context, color color.NRGBA, tmpl *template.Template) layout.Dimensions {
	rect := scaleRect(
		capturer.Bounds().Max, gtx.Constraints.Max,
		image.Rect(
			tmpl.MaxLoc.X,
			tmpl.MaxLoc.Y,
			tmpl.MaxLoc.X+tmpl.Width,
			tmpl.MaxLoc.Y+tmpl.Height,
		).Add(roiRect.Min),
	)
	return layoutRectAbsPos(gtx, color, rect)
}

func layoutTextRight(gtx layout.Context, color color.NRGBA, roiRect image.Rectangle, line int, txt string) layout.Dimensions {
	pos := image.Pt(
		roiRect.Max.X+fontSize/4,
		roiRect.Min.Y-0.5*fontSize+line*fontSize,
	)
	return layoutLabelAbsPos(gtx, color, pos, fontSize, txt)
}
