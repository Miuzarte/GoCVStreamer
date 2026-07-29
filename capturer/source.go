package capturer

import (
	"image"

	"gocv.io/x/gocv"
)

type Source interface {
	Bounds() image.Rectangle
	GetImage(img *image.RGBA) error
	ProvideMat(dst *gocv.Mat) bool
	FramesElapsed() int
	ResetFramesElapsed()
	Close() error
}
