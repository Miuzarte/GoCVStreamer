package ui

import (
	"image/color"

	"gioui.org/widget/material"

	"github.com/Miuzarte/GoCVStreamer/widgets"
)

const (
	FontSize        = 16
	BorderThickness = 2
)

type RGBA struct {
	R, G, B, A uint8
}

func (c RGBA) NRGBA() color.NRGBA {
	return color.NRGBA{R: c.R, G: c.G, B: c.B, A: c.A}
}

var (
	ColorWhite  = RGBA{0xFF, 0xFF, 0xFF, 0xFF}
	ColorBlack  = RGBA{0x00, 0x00, 0x00, 0xFF}
	ColorCoral  = RGBA{0xA6, 0x62, 0x61, 0xFF}
	ColorRed    = RGBA{0xFF, 0x00, 0x00, 0xFF}
	ColorYellow = RGBA{0xFF, 0xFF, 0x00, 0xFF}
	ColorGreen  = RGBA{0x00, 0xFF, 0x00, 0xFF}
	ColorCyan   = RGBA{0x00, 0xFF, 0xFF, 0xFF}
	ColorBlue   = RGBA{0x00, 0x00, 0xFF, 0xFF}
	ColorPurple = RGBA{0xFF, 0x00, 0xFF, 0xFF}
)

var Theme *material.Theme

func InitTheme() {
	t := material.NewTheme()
	t.Fg = ColorCoral.NRGBA()
	t.Bg = ColorWhite.NRGBA()
	t.ContrastFg = ColorWhite.NRGBA()
	t.ContrastBg = ColorCoral.NRGBA()
	t.Face = "Maple Mono Normal NF CN"
	Theme = t
	widgets.Theme = t
}
