package main

import (
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
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/Miuzarte/GoCVStreamer/fps"
	"github.com/Miuzarte/GoCVStreamer/template"
	"github.com/Miuzarte/GoCVStreamer/widgets"
)

const (
	fontSize        = 16
	borderThickness = 2
)

type rgba struct {
	R, G, B, A uint8
}

var (
	colorWhite = rgba{0xFF, 0xFF, 0xFF, 0xFF}
	colorBlack = rgba{0x00, 0x00, 0x00, 0xFF}

	colorCoral = rgba{0xA6, 0x62, 0x61, 0xFF}

	colorRed    = rgba{0xFF, 0x00, 0x00, 0xFF}
	colorYellow = rgba{0xFF, 0xFF, 0x00, 0xFF}
	colorGreen  = rgba{0x00, 0xFF, 0x00, 0xFF}
	colorCyan   = rgba{0x00, 0xFF, 0xFF, 0xFF}
	colorBlue   = rgba{0x00, 0x00, 0xFF, 0xFF}
	colorPurple = rgba{0xFF, 0x00, 0xFF, 0xFF}
)

var window app.Window

var (
	dScale unit.Metric
	mTheme = material.NewTheme()

	shortcuts = widgets.NewShortcuts(&window,
		widgets.Shortcut{
			Key: widgets.NewShortcut(0, 0, key.NameSpace),
			F:   shortcutListWeapons,
		},
		widgets.Shortcut{
			Key: widgets.NewShortcut(0, 0, "P", "p"),
			F:   shortcutPrintProcess,
		},
		widgets.Shortcut{
			Key: widgets.NewShortcut(0, 0, "F", "f"),
			F:   shortcutResetFreamsElapsed,
		},
		widgets.Shortcut{
			Key: widgets.NewShortcut(0, 0, "R", "r"),
			F:   shortcutResetRoi,
		},
		widgets.Shortcut{
			Key: widgets.NewShortcut(0, 0, "T", "t"),
			F:   shortcutSetWda,
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

func layoutDisplay(gtx layout.Context, img image.Image) {
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

var (
	cvFps = fps.NewCounter(time.Second / 2)
	cpu   float64
)

func layoutGocvInfo(gtx layout.Context) {
	const ms = float64(time.Millisecond)
	status := fmt.Sprintf(
		"| FPS: %.2f | 截图: %.1fms | %d | 绘制: %.1fms |\n| CPU: %.1f%% | 匹配: %.1fms/%d=%.2fms |",
		cvFps.Count(),
		float64(captureCost)/ms,
		capturer.FramesElapsed,
		float64(drawCost)/ms,

		cpu,
		float64(templatesMatchingCost)/ms, weaponMatched,
		float64(templatesMatchingCost)/float64(weaponMatched)/ms,
	)
	widgets.Label(fontSize*1.5, status).Layout(gtx)

	draggableRoi.Layout(gtx)

	roiRectScaled := scaleRect(
		capturer.Bounds().Max, gtx.Constraints.Max,
		draggableRoi.rect,
	)
	layoutRectAbsPos(gtx, color.NRGBA(colorCoral), roiRectScaled)

	labelPosScaled := scalePos(
		capturer.Bounds().Max, gtx.Constraints.Max,
		image.Pt(
			draggableRoi.rect.Min.X,
			draggableRoi.rect.Min.Y-(fontSize*2.5),
		),
	)
	if !draggableRoi.Dragging() {
		layoutLabelAbsPos(gtx, color.NRGBA(colorCoral), labelPosScaled, fontSize, "ROI")
	} else {
		layoutLabelAbsPos(gtx, color.NRGBA(colorCoral), labelPosScaled, fontSize, fmt.Sprint(draggableRoi.rect.Min))
		layoutLabelAbsPos(gtx, color.NRGBA(colorCoral), labelPosScaled.Add(roiRectScaled.Size()), fontSize, fmt.Sprint(draggableRoi.rect.Max))
	}

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
				draggableRoi.rect.Min.X, // 与ROI左对齐
				draggableRoi.rect.Max.Y,
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
		).Add(draggableRoi.rect.Min),
	)
	return layoutRectAbsPos(gtx, color, rect)
}

func layoutTextRight(gtx layout.Context, color color.NRGBA, roiRect image.Rectangle, line int, txt string) layout.Dimensions {
	pos := image.Pt(
		roiRect.Max.X+fontSize/4,
		roiRect.Min.Y+2-0.5*fontSize+line*fontSize,
	)
	return layoutLabelAbsPos(gtx, color, pos, fontSize, txt)
}

type DraggableRoi struct {
	widget.Draggable
	rect      image.Rectangle
	dragStart image.Rectangle
	printPos  bool
}

func (d *DraggableRoi) Layout(gtx layout.Context) layout.Dimensions {
	d.Draggable.Update(gtx)

	if d.Draggable.Dragging() {
		pos := d.Draggable.Pos()

		if pos.X == 0 && pos.Y == 0 {
			d.dragStart = d.rect
		} else {
			newRect := d.dragStart.Add(image.Pt(int(pos.X), int(pos.Y)))

			bounds := capturer.Bounds()
			if newRect.Min.X < bounds.Min.X {
				diff := bounds.Min.X - newRect.Min.X
				newRect = newRect.Add(image.Pt(diff, 0))
			}
			if newRect.Min.Y < bounds.Min.Y {
				diff := bounds.Min.Y - newRect.Min.Y
				newRect = newRect.Add(image.Pt(0, diff))
			}
			if newRect.Max.X > bounds.Max.X {
				diff := newRect.Max.X - bounds.Max.X
				newRect = newRect.Add(image.Pt(-diff, 0))
			}
			if newRect.Max.Y > bounds.Max.Y {
				diff := newRect.Max.Y - bounds.Max.Y
				newRect = newRect.Add(image.Pt(0, -diff))
			}

			d.rect = newRect
			d.printPos = true
		}

	} else if d.printPos {
		// 松开打印一次
		d.printPos = false
		log.Infof("current roi region: %v", d.rect)

	}

	roiRectScaled := scaleRect(
		capturer.Bounds().Max, gtx.Constraints.Max,
		d.rect,
	)
	defer op.Offset(roiRectScaled.Min).Push(gtx.Ops).Pop()
	return d.Draggable.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: roiRectScaled.Size()}
	}, nil)
}
