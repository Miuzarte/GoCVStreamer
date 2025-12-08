package widgets

import (
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"
)

type LabelStyle struct {
	material.LabelStyle
	Direction layout.Direction
	direction bool
}

func (l LabelStyle) Style(s font.Style) LabelStyle {
	l.LabelStyle.Font.Style = s
	return l
}

func (l LabelStyle) Weight(w font.Weight) LabelStyle {
	l.LabelStyle.Font.Weight = w
	return l
}

func (l LabelStyle) Alignment(a text.Alignment) LabelStyle {
	l.LabelStyle.Alignment = a
	return l
}

func (l LabelStyle) MaxLines(m int) LabelStyle {
	l.LabelStyle.MaxLines = m
	return l
}

func (l LabelStyle) SetDirection(d layout.Direction) LabelStyle {
	l.Direction = d
	l.direction = true
	return l
}

func (l LabelStyle) Layout(gtx layout.Context) layout.Dimensions {
	if l.direction {
		return l.Direction.Layout(gtx, l.LabelStyle.Layout)
	}
	return l.LabelStyle.Layout(gtx)
}

func Label(size unit.Sp, txt string) LabelStyle {
	return LabelStyle{LabelStyle: material.Label(Theme, size, txt)}
}
