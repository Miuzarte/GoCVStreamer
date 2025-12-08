package template

import (
	"fmt"
	"image"
	"math"
	"time"

	"gocv.io/x/gocv"
)

type Template struct {
	Raw           image.Image
	Mat           gocv.Mat
	Channels      int
	Width, Height int

	mask gocv.Mat

	result         gocv.Mat
	Cost           time.Duration
	MinVal, MaxVal float32
	MinLoc, MaxLoc image.Point
}

func (t *Template) IMReadFrom(path string, createMask bool) error {
	t.Close()
	t.mask = gocv.NewMat()
	t.result = gocv.NewMat()

	if !createMask {
		t.Mat = gocv.IMRead(path, gocv.IMReadColor)
	} else {
		img := gocv.IMRead(path, gocv.IMReadUnchanged)
		t.Mat = gocv.NewMat()
		switch img.Channels() {
		case 4:
			channels := gocv.Split(img)
			bgrChannels := []gocv.Mat{channels[0], channels[1], channels[2]}
			gocv.Merge(bgrChannels, &t.Mat)
			gocv.Threshold(channels[3], &t.mask, 64, 255, gocv.ThresholdBinary)
			if gocv.CountNonZero(t.mask) == 0 {
				return fmt.Errorf("unexpected zero mask")
			}
			for i := range channels {
				channels[i].Close()
			}

		case 3:
			img.ConvertTo(&t.Mat, gocv.MatTypeCV8UC3)
		case 1:
			img.ConvertTo(&t.Mat, gocv.MatTypeCV8UC1)

		default:
			return fmt.Errorf("unsupported number of channels: %d", img.Channels())
		}
	}

	var err error
	t.Raw, err = t.Mat.ToImage()
	if err != nil {
		return err
	}
	t.Channels = t.Mat.Channels()
	t.Width, t.Height = t.Mat.Cols(), t.Mat.Rows()

	return nil
}

func (t *Template) Close() error {
	var err1, err2, err3 error

	if !t.Mat.Closed() && !t.Mat.Empty() {
		err1 = t.Mat.Close()
	}
	if !t.mask.Closed() && !t.mask.Empty() {
		err2 = t.mask.Close()
	}
	if !t.result.Closed() && !t.mask.Empty() {
		err3 = t.result.Close()
	}

	if err1 != nil {
		return err1
	}
	if err2 != nil {
		return err2
	}
	if err3 != nil {
		return err3
	}
	return nil
}

func (t *Template) Match(image gocv.Mat, method gocv.TemplateMatchMode) error {
	tStart := time.Now()
	err := gocv.MatchTemplate(image, t.Mat, &t.result, method, t.mask)
	t.Cost = time.Since(tStart)
	if err != nil {
		return err
	}
	t.minMaxLoc()
	return nil
}

func (t *Template) minMaxLoc() {
	t.MinVal, t.MaxVal, t.MinLoc, t.MaxLoc = gocv.MinMaxLoc(t.result)
	minVal := float64(t.MinVal)
	if math.IsNaN(minVal) || math.IsInf(minVal, 0) {
		t.MinVal = 1.0
	}
	maxVal := float64(t.MaxVal)
	if math.IsNaN(maxVal) || math.IsInf(maxVal, 0) {
		t.MaxVal = -1.0
	}
}
