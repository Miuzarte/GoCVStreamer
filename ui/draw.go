package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/Miuzarte/GoCVStreamer/widgets"
)

type Drawer interface {
	Draw(gtx layout.Context, s DScale)
}

type DScale struct {
	Orig image.Point
	Curr image.Point
}

func NewDScale(orig, curr image.Point) DScale {
	return DScale{Orig: orig, Curr: curr}
}

func (s DScale) Ratio() float64 {
	return min(float64(s.Curr.X)/float64(s.Orig.X), float64(s.Curr.Y)/float64(s.Orig.Y))
}

func (s DScale) Pos(pos image.Point) image.Point {
	r := s.Ratio()
	displayW := int(float64(s.Orig.X) * r)
	displayH := int(float64(s.Orig.Y) * r)
	ox := (s.Curr.X - displayW) / 2
	oy := (s.Curr.Y - displayH) / 2
	return image.Pt(ox+int(float64(pos.X)*r), oy+int(float64(pos.Y)*r))
}

func (s DScale) Size(size image.Point) image.Point {
	r := s.Ratio()
	return image.Pt(int(float64(size.X)*r), int(float64(size.Y)*r))
}

func (s DScale) Rect(rect image.Rectangle) image.Rectangle {
	return image.Rectangle{Min: s.Pos(rect.Min), Max: s.Pos(rect.Max)}
}

func DrawBorder(gtx layout.Context, c color.NRGBA, rect image.Rectangle) layout.Dimensions {
	defer op.Offset(rect.Min).Push(gtx.Ops).Pop()
	box := widgets.NewBox()
	box.Thickness = BorderThickness
	box.BorderColor = c
	box.Border = true
	return box.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: rect.Size()}
	})
}

func DrawLabel(gtx layout.Context, c color.NRGBA, pos image.Point, size int, txt string) layout.Dimensions {
	defer op.Offset(pos).Push(gtx.Ops).Pop()
	label := widgets.Label(unit.Sp(size), txt)
	label.Color = c
	label.LabelStyle.Font.Weight = font.Bold
	return label.Layout(gtx)
}

func DrawImage(gtx layout.Context, pos image.Point, img image.Image) layout.Dimensions {
	defer op.Offset(pos).Push(gtx.Ops).Pop()
	imgOp := paint.NewImageOp(img)
	imgOp.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	return layout.Dimensions{Size: imgOp.Size()}
}

func DrawTextRight(gtx layout.Context, c color.NRGBA, roiRect image.Rectangle, line int, txt string) layout.Dimensions {
	pos := image.Pt(
		roiRect.Max.X+FontSize/4,
		roiRect.Min.Y+2-FontSize/2+line*FontSize,
	)
	return DrawLabel(gtx, c, pos, FontSize, txt)
}

func DrawList(gtx layout.Context, lines []string) layout.Dimensions {
	label := widgets.Label(unit.Sp(FontSize*1.5), "")
	for _, l := range lines {
		label.Text += l + "\n"
	}
	return label.Layout(gtx)
}

func FormatPct(v float32) string {
	return fmt.Sprintf("%.2f%%", v*100)
}

func DrawLine(gtx layout.Context, c color.NRGBA, from, to image.Point) layout.Dimensions {
	defer op.Offset(from).Push(gtx.Ops).Pop()

	dx := float64(to.X - from.X)
	dy := float64(to.Y - from.Y)
	length := float32(math.Sqrt(dx*dx + dy*dy))
	if length < 1 {
		return layout.Dimensions{}
	}

	angle := float32(math.Atan2(dy, dx))

	defer op.Affine(f32.Affine2D{}.Rotate(f32.Pt(0, 0), angle)).Push(gtx.Ops).Pop()

	halfT := BorderThickness / 2
	defer clip.Rect{Min: image.Pt(0, -halfT), Max: image.Pt(int(length), halfT)}.Push(gtx.Ops).Pop()

	paint.ColorOp{Color: c}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)

	return layout.Dimensions{}
}
