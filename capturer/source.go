package capturer

import (
	"image"

	"gocv.io/x/gocv"
)

type Source interface {
	Bounds() image.Rectangle
	GetImage(img *image.RGBA) error
	GetImageTimeout(img *image.RGBA, timeoutMs uint) error
	ProvideMat(dst *gocv.Mat) bool
	FramesElapsed() int
	ResetFramesElapsed()
	Close() error
}
