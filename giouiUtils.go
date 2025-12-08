package main

import (
	"image"
	"image/color"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"github.com/Miuzarte/GoCVStreamer/widgets"
)

func ratioOfTwoPoints(orig, curr image.Point) float64 {
	return min(float64(curr.X)/float64(orig.X), float64(curr.Y)/float64(orig.Y))
}

func scaleSize(orig, curr image.Point, size image.Point) image.Point {
	ratio := ratioOfTwoPoints(orig, curr)

	scaledX := int(float64(size.X) * ratio)
	scaledY := int(float64(size.Y) * ratio)

	return image.Pt(scaledX, scaledY)
}

func scalePos(orig, curr image.Point, pos image.Point) image.Point {
	ratio := ratioOfTwoPoints(orig, curr)

	displayWidth := int(float64(orig.X) * ratio)
	displayHeight := int(float64(orig.Y) * ratio)
	offsetX := (curr.X - displayWidth) / 2
	offsetY := (curr.Y - displayHeight) / 2

	scaledX := offsetX + int(float64(pos.X)*ratio)
	scaledY := offsetY + int(float64(pos.Y)*ratio)

	return image.Pt(scaledX, scaledY)
}

func scaleRect(orig, curr image.Point, rect image.Rectangle) image.Rectangle {
	ratio := ratioOfTwoPoints(orig, curr)

	displayWidth := int(float64(orig.X) * ratio)
	displayHeight := int(float64(orig.Y) * ratio)
	offsetX := (curr.X - displayWidth) / 2
	offsetY := (curr.Y - displayHeight) / 2

	scaledMinX := offsetX + int(float64(rect.Min.X)*ratio)
	scaledMinY := offsetY + int(float64(rect.Min.Y)*ratio)
	scaledMaxX := offsetX + int(float64(rect.Max.X)*ratio)
	scaledMaxY := offsetY + int(float64(rect.Max.Y)*ratio)

	return image.Rect(scaledMinX, scaledMinY, scaledMaxX, scaledMaxY)
}

func layoutRectAbsPos(gtx layout.Context, color color.NRGBA, rect image.Rectangle) layout.Dimensions {
	defer op.Offset(rect.Min).Push(gtx.Ops).Pop()
	box := widgets.NewBox()
	box.Thickness = borderThickness
	box.BorderColor = color
	box.Border = true
	return box.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: rect.Size()}
	})
}

func layoutLabelAbsPos(gtx layout.Context, color color.NRGBA, pos image.Point, size unit.Sp, txt string) layout.Dimensions {
	defer op.Offset(pos).Push(gtx.Ops).Pop()
	label := widgets.Label(size, txt)
	label.Color = color
	label.LabelStyle.Font.Weight = font.Bold
	return label.Layout(gtx)
}

func layoutImageAbsPos(gtx layout.Context, pos image.Point, img image.Image) layout.Dimensions {
	defer op.Offset(pos).Push(gtx.Ops).Pop()
	imgOp := paint.NewImageOp(img)
	imgOp.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	return layout.Dimensions{Size: imgOp.Size()}
}
