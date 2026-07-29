package capturer

import (
	"fmt"
	"image"
	"sync"

	"gocv.io/x/gocv"
)

type ObsCameraSource struct {
	mu      sync.Mutex
	cam     *gocv.VideoCapture
	bounds  image.Rectangle
	frames  int
	bgrMat  gocv.Mat
	rgbaMat gocv.Mat
}

func NewObsCamera(index, reqW, reqH int) (*ObsCameraSource, error) {
	cam, err := gocv.OpenVideoCaptureWithAPI(index, gocv.VideoCaptureDshow)
	if err != nil {
		return nil, fmt.Errorf("failed to open camera %d: %w", index, err)
	}

	if reqW > 0 {
		cam.Set(gocv.VideoCaptureFrameWidth, float64(reqW))
	}
	if reqH > 0 {
		cam.Set(gocv.VideoCaptureFrameHeight, float64(reqH))
	}

	w := int(cam.Get(gocv.VideoCaptureFrameWidth))
	h := int(cam.Get(gocv.VideoCaptureFrameHeight))
	if w <= 0 || h <= 0 {
		cam.Close()
		return nil, fmt.Errorf("camera %d returned invalid resolution: %dx%d", index, w, h)
	}

	log.Info().
		Int("index", index).
		Int("width", w).
		Int("height", h).
		Msg("obs camera opened")

	return &ObsCameraSource{
		cam:     cam,
		bounds:  image.Rect(0, 0, w, h),
		bgrMat:  gocv.NewMat(),
		rgbaMat: gocv.NewMat(),
	}, nil
}

func (o *ObsCameraSource) Bounds() image.Rectangle {
	return o.bounds
}

func (o *ObsCameraSource) GetImage(img *image.RGBA) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.cam.Read(&o.bgrMat) {
		return fmt.Errorf("failed to read frame from camera")
	}
	gocv.CvtColor(o.bgrMat, &o.rgbaMat, gocv.ColorBGRToRGBA)

	data, err := o.rgbaMat.DataPtrUint8()
	if err != nil {
		return fmt.Errorf("failed to get mat data: %w", err)
	}
	copy(img.Pix, data)
	o.frames++
	return nil
}

func (o *ObsCameraSource) GetImageTimeout(img *image.RGBA, timeoutMs uint) error {
	return o.GetImage(img)
}

func (o *ObsCameraSource) ProvideMat(dst *gocv.Mat) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.bgrMat.CopyTo(dst)
	return true
}

func (o *ObsCameraSource) FramesElapsed() int {
	return o.frames
}

func (o *ObsCameraSource) ResetFramesElapsed() {
	o.frames = 0
}

func (o *ObsCameraSource) Close() error {
	o.bgrMat.Close()
	o.rgbaMat.Close()
	return o.cam.Close()
}
