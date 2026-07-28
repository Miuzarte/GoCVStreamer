package libyuv

import (
	"fmt"
	"image"
	"image/draw"
	"slices"
	"testing"
	"time"

	"github.com/kbinani/screenshot"
)

func TestResizeRgba(t *testing.T) {
	n := screenshot.NumActiveDisplays()
	if n == 0 {
		t.Skip("no active display")
	}

	var biggestBounds image.Rectangle
	for i := range n {
		b := screenshot.GetDisplayBounds(i)
		a := b.Dx() * b.Dy()
		if a > biggestBounds.Dx()*biggestBounds.Dy() {
			biggestBounds = b
		}
	}

	img, err := screenshot.CaptureRect(biggestBounds)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	srcW := img.Bounds().Dx()
	srcH := img.Bounds().Dy()
	cropSize := 1280
	if srcW < cropSize || srcH < cropSize {
		t.Skipf("display too small: %dx%d", srcW, srcH)
	}

	ox := (srcW - cropSize) / 2
	oy := (srcH - cropSize) / 2

	cropImg := image.NewRGBA(image.Rect(0, 0, cropSize, cropSize))
	draw.Draw(cropImg, cropImg.Bounds(), img, image.Pt(ox, oy), draw.Src)

	fmt.Printf("capture: %dx%d, crop: %dx%d (+%d,+%d)\n", srcW, srcH, cropSize, cropSize, ox, oy)

	const rounds = 10
	durations := make([]time.Duration, rounds)

	for i := range rounds {
		src := image.NewRGBA(image.Rect(0, 0, cropSize, cropSize))
		copy(src.Pix, cropImg.Pix)

		t0 := time.Now()
		dst := ResizeRGBA(src, 640, 640)
		d := time.Since(t0)
		durations[i] = d

		_ = dst
	}

	slices.Sort(durations)

	var total time.Duration
	for _, d := range durations {
		total += d
	}
	avg := total / rounds

	fmt.Printf("rounds=%d  min=%v  max=%v  avg=%v\n", rounds, durations[0], durations[rounds-1], avg)
}
