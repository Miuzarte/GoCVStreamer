package capturer

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"runtime"
	"sync"
	"time"

	"github.com/Miuzarte/GoCVStreamer/fps"
	"github.com/kirides/go-d3d/outputduplication"
	"gocv.io/x/gocv"
)

var imageToMatWarnOnce sync.Once

type Stats struct {
	FPS        float64
	FrameTime  time.Duration
	Cost       time.Duration
	FrameCount int
}

func (s Stats) CostMs() float64 {
	return float64(s.Cost) / float64(time.Millisecond)
}

type Frame struct {
	RGBA *image.RGBA
	Mat  gocv.Mat
	ID   uint64
}

type RequestTag int

const (
	TagOpenCV RequestTag = iota
	TagYOLO
)

type CaptureReq struct {
	Tag      RequestTag
	Interval time.Duration
}

type Server struct {
	duplicator *DxgiDesktopDuplicator
	fp         fps.Counter

	mu         sync.RWMutex
	frame      Frame
	screenRGBA *image.RGBA

	req         chan CaptureReq
	stats       Stats
	onFrame     func()
	cvtCode     gocv.ColorConversionCode

	lastTag     RequestTag
	lastCapture time.Time
}

func NewServer(d *DxgiDesktopDuplicator, mode gocv.IMReadFlag, onFrame func()) *Server {
	bounds := d.Bounds()
	cvtCode := gocv.ColorRGBAToBGR
	if mode == gocv.IMReadGrayScale {
		cvtCode = gocv.ColorRGBAToGray
	}
	return &Server{
		duplicator: d,
		fp:         fps.NewCounter(time.Second),
		frame:      Frame{Mat: gocv.NewMat()},
		screenRGBA: image.NewRGBA(bounds),
		req:        make(chan CaptureReq, 2),
		onFrame:    onFrame,
		cvtCode:    cvtCode,
	}
}

func (s *Server) Bounds() image.Rectangle {
	return s.duplicator.Bounds()
}

func (s *Server) Request(tag RequestTag, interval time.Duration) {
	select {
	case s.req <- CaptureReq{Tag: tag, Interval: interval}:
	default:
	}
}

func (s *Server) ReadRGBA() *image.RGBA {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.frame.RGBA
}

func (s *Server) ReadMat() gocv.Mat {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.frame.Mat
}

func (s *Server) ReadFrameID() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.frame.ID
}

func (s *Server) ReadScreen() *image.RGBA {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.screenRGBA
}

func (s *Server) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stats
}

func (s *Server) FramesElapsed() int {
	return s.duplicator.FramesElapsed
}

func (s *Server) ResetFramesElapsed() {
	s.duplicator.FramesElapsed = 0
}

func (s *Server) Run(ctx context.Context) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	rawRGBA := image.NewRGBA(s.duplicator.Bounds())

	for {
		var req CaptureReq
		select {
		case <-ctx.Done():
			return
		case req = <-s.req:
		}

		if req.Tag != s.lastTag && time.Since(s.lastCapture) < req.Interval {
			continue
		}
		s.lastTag = req.Tag
		s.lastCapture = time.Now()

		tStart := time.Now()

		err := s.duplicator.GetImage(rawRGBA)
		if err == outputduplication.ErrNoImageYet {
			continue
		}
		if err != nil {
			log.Error().Err(err).Msg("capture error")
			continue
		}

		s.mu.Lock()
		s.frame.RGBA = rawRGBA
		s.frame.ID++
		copy(s.screenRGBA.Pix, rawRGBA.Pix)

		err = s.imageToMat(rawRGBA, &s.frame.Mat)
		if err != nil {
			log.Error().Err(err).Msg("failed to convert image to mat")
			s.mu.Unlock()
			continue
		}

		s.stats.Cost = time.Since(tStart)
		s.stats.FPS, s.stats.FrameTime = s.fp.Count()
		s.stats.FrameCount = s.duplicator.FramesElapsed
		s.mu.Unlock()

		if s.onFrame != nil {
			s.onFrame()
		}
	}
}

func (s *Server) Close() error {
	s.frame.Mat.Close()
	return s.duplicator.Close()
}

func (s *Server) imageToMat(img image.Image, dst *gocv.Mat) (err error) {
	var src gocv.Mat

	bounds := img.Bounds()
	x := bounds.Dx()
	y := bounds.Dy()

	switch img.ColorModel() {
	case color.RGBAModel:
		m, res := img.(*image.RGBA)
		if true != res {
			return fmt.Errorf("image color format error")
		}
		// speed up the conversion process of RGBA format
		src, err = gocv.NewMatFromBytes(y, x, gocv.MatTypeCV8UC4, m.Pix)
		if err != nil {
			return err
		}
		defer src.Close()

		return gocv.CvtColor(src, dst, s.cvtCode)

	default:
		imageToMatWarnOnce.Do(func() {
			log.Warn().Msg("unexpected image color model, conversion performance may be affected")
		})
		if s.cvtCode == gocv.ColorRGBAToGray {
			data := make([]byte, 0, x*y)
			for j := bounds.Min.Y; j < bounds.Max.Y; j++ {
				for i := bounds.Min.X; i < bounds.Max.X; i++ {
					r, g, b, _ := img.At(i, j).RGBA()
					gray := byte((19595*uint32(r) + 38470*uint32(g) + 7471*uint32(b)) >> 16)
					data = append(data, gray)
				}
			}
			src, err = gocv.NewMatFromBytes(y, x, gocv.MatTypeCV8UC1, data)
			if err != nil {
				return err
			}
			defer src.Close()
			src.CopyTo(dst)
			return nil
		}
		data := make([]byte, 0, x*y*3)
		for j := bounds.Min.Y; j < bounds.Max.Y; j++ {
			for i := bounds.Min.X; i < bounds.Max.X; i++ {
				r, g, b, _ := img.At(i, j).RGBA()
				data = append(data, byte(b>>8), byte(g>>8), byte(r>>8))
			}
		}
		src, err = gocv.NewMatFromBytes(y, x, gocv.MatTypeCV8UC3, data)
		if err != nil {
			return err
		}
		defer src.Close()
		src.CopyTo(dst)
		return nil
	}
}
