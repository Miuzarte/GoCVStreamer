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

	w "github.com/Miuzarte/GoCVStreamer/weapon"
	"github.com/Miuzarte/GoCVStreamer/widgets"
	"github.com/getcharzp/go-vision/yolo26"
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
	dScale unit.Metric // 处理缩放时用, 暂时用不到
	mTheme = material.NewTheme()
)

var shortcuts widgets.Shortcuts

func init() {
	mTheme.Fg = color.NRGBA(colorCoral)
	mTheme.Bg = color.NRGBA(colorWhite)
	mTheme.ContrastFg = color.NRGBA(colorWhite)
	mTheme.ContrastBg = color.NRGBA(colorCoral)
	mTheme.Face = "Maple Mono Normal NF CN"
	widgets.Theme = mTheme

	if *nogui {
		return
	}

	shortcuts = widgets.NewShortcuts(&window,
		widgets.NewShortcut(key.NameEscape).
			Do(shortcutQuitGui),

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
}

// layoutDisplay 绘制截图
func layoutDisplay(gtx layout.Context, img image.Image) {
	gtxBounds := gtx.Constraints.Max
	gtxW, gtxH := gtxBounds.X, gtxBounds.Y

	imgBounds := img.Bounds()
	imgW, imgH := imgBounds.Dx(), imgBounds.Dy()

	// 取最小的缩放因子
	scale := min(float32(gtxW)/float32(imgW), float32(gtxH)/float32(imgH))

	// 实际绘制大小
	drawW, drawH := int(float32(imgW)*scale), int(float32(imgH)*scale)

	// 居中
	defer op.Offset(image.Pt((gtxW-drawW)/2, (gtxH-drawH)/2)).Push(gtx.Ops).Pop()
	// 缩放
	defer op.Affine(f32.AffineId().Scale(f32.Pt(0, 0), f32.Pt(scale, scale))).Push(gtx.Ops).Pop()

	// 绘制
	paint.NewImageOp(img).Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
}

const METRICS_TEMPLATE = //
`| Screen: {{printf "%.0ffps(%.1fms)" .ScreenFPS .ScreenCostMs}} | OpenCV: {{printf "%.0ffps(%.1fms)" .MatchFPS .MatchCostMs}} | YOLO: {{printf "%.0ffps" .PersonFPS}}/{{.PersonDetCount}}({{printf "%.1fms" .PersonDetCost}}) | 0x{{printf "%04X" .FramesElapsed}} |{{if .Debugging}} DEBUG |{{end}}
| CPU: {{printf "%04.1f" .Cpu}}% | GC: {{printf "%d(avg: %.2fus, last: %.2fs)" .NumGc .PauseAvgUs .SinceLastGcS}} | 匹配: {{printf "%.1f" .WeaponsMatchingCostTotalMs}}ms/{{.WeaponsMatched}}={{printf "%.2f" .WeaponsMatchingCostAvgMs}}ms |`

var (
	metricsTmpl = textTemplate.Must(textTemplate.New("Metrics").Parse(METRICS_TEMPLATE))
	metrics     Metrics
	metricsBuf  bytes.Buffer
)

// layoutMetrics 绘制覆盖层元素
func layoutMetrics(gtx layout.Context) {
	metrics = SnapshotMetrics()
	metricsBuf.Reset()
	panicIf(metricsTmpl.Execute(&metricsBuf, &metrics))

	metricsBuf.WriteString("\n\n")
	if inputting {
		if !inputMainOrAlt {
			metricsBuf.WriteString("|M|: ")
		} else {
			metricsBuf.WriteString("|A|: ")
		}
		metricsBuf.Write(inputBuf.Bytes())
	}

	metricsBuf.WriteString("\n\n")
	metricsBuf.WriteString(currentWeaponDisplay)
	if r6sEngine != nil {
		fmt.Fprintf(&metricsBuf, "\nOffset: %d\nJitter: %d", r6sEngine.SpeedOffset, r6sEngine.HoriJitterBase)
	}

	widgets.Label(
		FONT_SIZE*1.5,
		// unsafe.String(unsafe.SliceData(gocvInfoBuf.Bytes()), gocvInfoBuf.Len()),
		metricsBuf.String(),
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

	weaponsMu.RLock()
	defer weaponsMu.RUnlock()

	colorPos := colorGreen
	colorNeg := colorCyan
	min, max := weapons.MinMaxIndex()
	weaponPos := weapons[weaponIndex]
	weaponNeg := weapons[min]
	if !weaponFound {
		// 无可信匹配时, 黄框显示最高匹配的模板
		colorPos = colorYellow
		weaponPos = weapons[max]
	}

	if weaponPos.Template.MaxVal >= 0.5 {
		layoutOpenCVResult(gtx, roiRectScaled, weaponPos, color.NRGBA(colorPos), 0)
		if DRAW_NEGATIVE_RESULT {
			layoutOpenCVResult(gtx, roiRectScaled, weaponNeg, color.NRGBA(colorNeg), weaponPos.Template.Height)
		}
	}

	if yoloEngine != nil {
		detResults, _, _ := yoloEngine.Snapshot()
		layoutYoloResult(gtx, detResults)
	}
}

func layoutOpenCVResult(gtx layout.Context, roiRectScaled image.Rectangle, weapon *w.Weapon, color color.NRGBA, tmplPosOffset int) {
	tmplPosPos := scalePos(
		capturer.Bounds().Max, gtx.Constraints.Max,
		image.Pt(
			roiRect.Min.X, // 与ROI左对齐
			roiRect.Max.Y+tmplPosOffset,
		),
	)
	tmplPosPos.Y += BORDER_THICKNESS / 2
	layoutImageAbsPos(gtx, tmplPosPos, weapon.Template.Raw) // 匹配的模板本身
	rect := scaleRect(
		capturer.Bounds().Max, gtx.Constraints.Max,
		image.Rect(
			weapon.Template.MaxLoc.X,
			weapon.Template.MaxLoc.Y,
			weapon.Template.MaxLoc.X+weapon.Template.Width,
			weapon.Template.MaxLoc.Y+weapon.Template.Height,
		).Add(roiRect.Min),
	)
	layoutRectAbsPos(gtx, color, rect)
	layoutTextRight(gtx, color, roiRectScaled, 0, weapon.Name)
	layoutTextRight(gtx, color, roiRectScaled, 1, fmt.Sprintf("%.2f%%", weapon.Template.MaxVal*100))
}

func layoutTextRight(gtx layout.Context, color color.NRGBA, roiRect image.Rectangle, line int, txt string) layout.Dimensions {
	pos := image.Pt(
		roiRect.Max.X+FONT_SIZE/4,
		roiRect.Min.Y+2-0.5*FONT_SIZE+line*FONT_SIZE,
	)
	return layoutLabelAbsPos(gtx, color, pos, FONT_SIZE, txt)
}

func layoutYoloResult(gtx layout.Context, detResults []yolo26.DetResult) {
	for _, det := range detResults {
		rect := scaleRect(
			capturer.Bounds().Max, gtx.Constraints.Max,
			det.Box,
		)
		layoutRectAbsPos(gtx, color.NRGBA(colorGreen), rect)
		labelPos := image.Pt(rect.Min.X, rect.Min.Y-FONT_SIZE)
		layoutLabelAbsPos(gtx, color.NRGBA(colorGreen), labelPos, FONT_SIZE, fmt.Sprintf("%.0f%%", det.Score*100))
	}
}
