package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	textTemplate "text/template"
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
)

const (
	FONT_SIZE        = 16
	BORDER_THICKNESS = 2
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
		widgets.NewShortcut(key.NameSpace).
			Do(shortcutListWeapons),

		widgets.NewShortcut("W", "w").
			Do(shortcutReloadWeapons),

		widgets.NewShortcut("P", "p").
			Do(shortcutPrintProcess),

		widgets.NewShortcut("F", "f").
			Do(shortcutResetFreamsElapsed),

		widgets.NewShortcut("D", "d").
			Do(shortcutToggleDraw),

		widgets.NewShortcut("B", "b").
			Do(shortcutToggleDebug),

		widgets.NewShortcut("R", "r",
			key.NameUpArrow, key.NameDownArrow,
			key.NameLeftArrow, key.NameRightArrow).
			Do(shortcutMoveRoiRect),

		widgets.NewShortcut("T", "t").
			Do(shortcutSetWda),

		widgets.NewShortcut("I", "i",
			"0", "1", "2", "3", "4",
			"5", "6", "7", "8", "9",
			".", "-", key.NameReturn,
			key.NameDeleteBackward).
			Do(shortcutStartInput),
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
	defer op.Affine(f32.AffineId().Scale(f32.Pt(0, 0), f32.Pt(scale, scale))).Push(gtx.Ops).Pop()

	// 绘制
	paint.NewImageOp(img).Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
}

var (
	fpsCounter = fps.NewCounter(SAMPLE_FREQUENCY)
	cpu        float64
)

type GocvInfo struct {
	FpsCount      float64
	FrametimeMs   float64
	CaptureCostMs float64
	FramesElapsed int
	Debugging     bool

	Cpu float64

	NumGc        int
	PauseAvgUs   float64
	SinceLastGcS float64

	WeaponsMatchingCostTotalMs float64
	WeaponsMatched             int
	WeaponsMatchingCostAvgMs   float64
}

const GOCV_INFO_TEMPLATE = //
`| FPS: {{printf "%05.2f(%.1fms)" .FpsCount .FrametimeMs}} | 截图: {{printf "%.1f" .CaptureCostMs}}ms | 0x{{printf "%04X" .FramesElapsed}} |{{if .Debugging}} DEBUG |{{end}}
| CPU: {{printf "%04.1f" .Cpu}}% | GC: {{printf "%d(avg: %.2fus, last: %.2fs)" .NumGc .PauseAvgUs .SinceLastGcS}} | 匹配: {{printf "%.1f" .WeaponsMatchingCostTotalMs}}ms/{{.WeaponsMatched}}={{printf "%.2f" .WeaponsMatchingCostAvgMs}}ms |`

var (
	gocvInfoTmpl = textTemplate.Must(textTemplate.New("GocvInfo").Parse(GOCV_INFO_TEMPLATE))
	gocvInfo     GocvInfo
	gocvInfoBuf  bytes.Buffer
)

func layoutGocvInfo(gtx layout.Context) {
	weaponsMu.RLock()
	defer weaponsMu.RUnlock()

	const ms = float64(time.Millisecond)
	const us = float64(time.Microsecond)

	gocvInfoBuf.Reset()

	var frametime time.Duration
	gocvInfo.FpsCount, frametime = fpsCounter.Count()
	gocvInfo.FrametimeMs = float64(frametime) / ms
	gocvInfo.CaptureCostMs = float64(captureCost) / ms
	gocvInfo.FramesElapsed = capturer.FramesElapsed
	gocvInfo.Debugging = debugging

	gocvInfo.Cpu = cpu

	gocvInfo.NumGc = int(lastGCStats.NumGC)
	gocvInfo.PauseAvgUs = float64(lastGCStats.PauseTotal) / float64(lastGCStats.NumGC) / us
	gocvInfo.SinceLastGcS = time.Since(lastGCStats.LastGC).Seconds()

	gocvInfo.WeaponsMatchingCostTotalMs = float64(weaponsMatchingCost) / ms
	gocvInfo.WeaponsMatched = weaponsMatched
	gocvInfo.WeaponsMatchingCostAvgMs = float64(weaponsMatchingCost) / float64(weaponsMatched) / ms

	panicIf(gocvInfoTmpl.Execute(&gocvInfoBuf, &gocvInfo))

	gocvInfoBuf.WriteString("\n\n")
	if inputting {
		if !inputMainOrAlt {
			gocvInfoBuf.WriteString("|M|: ")
		} else {
			gocvInfoBuf.WriteString("|A|: ")
		}
		gocvInfoBuf.Write(inputBuf.Bytes())
	}

	gocvInfoBuf.WriteString("\n\n")
	gocvInfoBuf.Write(luaFileContent)

	widgets.Label(
		FONT_SIZE*1.5,
		// unsafe.String(unsafe.SliceData(gocvInfoBuf.Bytes()), gocvInfoBuf.Len()),
		gocvInfoBuf.String(),
	).Layout(gtx)

	roiRectScaled := scaleRect(
		capturer.Bounds().Max, gtx.Constraints.Max,
		roiRect,
	)
	layoutRectAbsPos(gtx, color.NRGBA(colorCoral), roiRectScaled)

	labelPosScaled := scalePos(
		capturer.Bounds().Max, gtx.Constraints.Max,
		image.Pt(
			roiRect.Min.X,
			roiRect.Min.Y,
		),
	)
	labelPosScaled.Y -= FONT_SIZE * 1.25
	if time.Now().Before(showPosTill) {
		layoutLabelAbsPos(gtx, color.NRGBA(colorCoral), labelPosScaled, FONT_SIZE, fmt.Sprint(roiRect))
	} else {
		layoutLabelAbsPos(gtx, color.NRGBA(colorCoral), labelPosScaled, FONT_SIZE, "ROI")
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

	if weaponPos.Template.MaxVal >= 0.5 {
		tmplPosPos := scalePos(
			capturer.Bounds().Max, gtx.Constraints.Max,
			image.Pt(
				roiRect.Min.X, // 与ROI左对齐
				roiRect.Max.Y,
			),
		)
		tmplPosPos.Y += BORDER_THICKNESS / 2
		layoutImageAbsPos(gtx, tmplPosPos, weaponPos.Template.Raw) // 匹配的模板本身
		layoutResultRect(gtx, color.NRGBA(colorPos), &weaponPos.Template)
		layoutTextRight(gtx, color.NRGBA(colorPos), roiRectScaled, 0, weaponPos.Name)
		layoutTextRight(gtx, color.NRGBA(colorPos), roiRectScaled, 1, fmt.Sprintf("%.2f%%", weaponPos.Template.MaxVal*100))

		if DRAW_NEGATIVE_RESULT {
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
		roiRect.Max.X+FONT_SIZE/4,
		roiRect.Min.Y+2-0.5*FONT_SIZE+line*FONT_SIZE,
	)
	return layoutLabelAbsPos(gtx, color, pos, FONT_SIZE, txt)
}
