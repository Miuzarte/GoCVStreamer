package widgets

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

type Box struct {
	Radius                       int
	Thickness                    float32
	Inset                        layout.Inset
	BorderColor, BackgroundColor color.NRGBA
	Border, Background           bool
}

func NewBox() Box {
	return Box{
		Radius:          0,
		Thickness:       2,
		Inset:           layout.Inset{},
		BorderColor:     Theme.ContrastBg,
		BackgroundColor: Theme.Bg,
		Border:          true,
		Background:      false,
	}
}

func (b Box) Layout(gtx layout.Context, widget layout.Widget) layout.Dimensions {
	b.Thickness = max(b.Thickness, 1)

	macro := op.Record(gtx.Ops)
	gtx2 := gtx
	gtx2.Constraints.Min = image.Point{}
	dims := b.Inset.Layout(gtx2, widget)
	call := macro.Stop()

	gtx2.Constraints.Min.X = max(gtx2.Constraints.Min.X, dims.Size.X)
	gtx2.Constraints.Min.Y = max(gtx2.Constraints.Min.Y, dims.Size.Y)

	background := func(gtx layout.Context) layout.Dimensions {
		bg := clip.RRect{
			SE: b.Radius, SW: b.Radius,
			NW: b.Radius, NE: b.Radius,
			Rect: image.Rectangle{image.Point{0, 0}, dims.Size},
		}
		outline := clip.Stroke{
			Path:  bg.Path(gtx.Ops),
			Width: b.Thickness,
		}
		if b.Border {
			paint.FillShape(gtx.Ops, b.BorderColor, outline.Op())
		}
		if b.Background {
			paint.FillShape(gtx.Ops, b.BackgroundColor, bg.Op(gtx.Ops))
		}
		return dims
	}

	content := func(gtx layout.Context) layout.Dimensions {
		call.Add(gtx.Ops)
		return dims
	}

	return layout.Background{}.Layout(gtx2, background, content)
}
