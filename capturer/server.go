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
	"github.com/Miuzarte/GoCVStreamer/timing"
	"github.com/kirides/go-d3d/outputduplication"
	"gocv.io/x/gocv"
)

type Config struct {
	MinFps        int
	DisableOpenCV bool
}

var imageToMatWarnOnce sync.Once

type Stats struct {
	FPS        float64
	FrameTime  time.Duration
	Cost       time.Duration
	FrameCount int
}

type Frame struct {
	rgba *image.RGBA
	mat  gocv.Mat
	id   uint64
}

type Server struct {
	source Source
	fp     fps.Counter

	mu         sync.RWMutex
	frame      Frame
	screenRGBA *image.RGBA

	stats   Stats
	onFrame func()
	cvtCode gocv.ColorConversionCode
	cfg     Config

	targetFps    int
	targetExpiry time.Time
	targetMu     sync.Mutex

	noOpenCV bool

	diagGetImage   *timing.Diag
	diagImageToMat *timing.Diag
}

func NewServer(src Source, cfg Config, mode gocv.IMReadFlag, onFrame func()) *Server {
	bounds := src.Bounds()
	cvtCode := gocv.ColorRGBAToBGR
	if mode == gocv.IMReadGrayScale {
		cvtCode = gocv.ColorRGBAToGray
	}
	if cfg.MinFps <= 0 {
		cfg.MinFps = 1
	}
	s := &Server{
		source:     src,
		fp:         fps.NewCounter(time.Second),
		screenRGBA: image.NewRGBA(bounds),
		onFrame:    onFrame,
		cvtCode:    cvtCode,
		cfg:        cfg,
		targetFps:  cfg.MinFps,
		noOpenCV:   cfg.DisableOpenCV,

		diagGetImage:   timing.NewDiag("GetImage"),
		diagImageToMat: timing.NewDiag("ImageToMat"),
	}
	if !cfg.DisableOpenCV {
		s.frame.mat = gocv.NewMat()
	}
	return s
}

func (s *Server) Bounds() image.Rectangle {
	return s.source.Bounds()
}

func (s *Server) RaiseCeiling(fps int) {
	s.targetMu.Lock()
	defer s.targetMu.Unlock()
	if fps >= s.targetFps {
		s.targetFps = fps
		s.targetExpiry = time.Now().Add(3 * time.Second)
	}
}

func (s *Server) ReadRgba() *image.RGBA {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.frame.rgba
}

func (s *Server) ReadMat() gocv.Mat {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.frame.mat
}

func (s *Server) ReadFrameId() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.frame.id
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
	return s.source.FramesElapsed()
}

func (s *Server) ResetFramesElapsed() {
	s.source.ResetFramesElapsed()
}

func (s *Server) Run(ctx context.Context) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	rawRGBA := image.NewRGBA(s.source.Bounds())

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		s.targetMu.Lock()
		if time.Now().After(s.targetExpiry) {
			s.targetFps = s.cfg.MinFps
		}
		fps := s.targetFps
		s.targetMu.Unlock()

		tStart := time.Now()

		err := s.source.GetImage(rawRGBA)
		if err == outputduplication.ErrNoImageYet {
			continue
		}
		if err != nil {
			log.Error().
				Err(err).
				Msg("capture error")
			time.Sleep(100 * time.Millisecond)
			continue
		}
		s.diagGetImage.Observe(time.Since(tStart), log)

		s.mu.Lock()
		s.frame.rgba = rawRGBA
		s.frame.id++
		copy(s.screenRGBA.Pix, rawRGBA.Pix)

		if !s.noOpenCV {
			tImg := time.Now()
			if s.source.ProvideMat(&s.frame.mat) {
				if s.cvtCode == gocv.ColorRGBAToGray {
					tmp := s.frame.mat.Clone()
					gocv.CvtColor(tmp, &s.frame.mat, gocv.ColorBGRToGray)
					tmp.Close()
				}
			} else {
				err = s.imageToMat(rawRGBA, &s.frame.mat)
				if err != nil {
					log.Error().Err(err).Msg("failed to convert image to mat")
					s.mu.Unlock()
					continue
				}
			}
			s.diagImageToMat.Observe(time.Since(tImg), log)
		}

		s.stats.Cost = time.Since(tStart)
		s.stats.FPS, s.stats.FrameTime = s.fp.Count()
		s.stats.FrameCount = s.source.FramesElapsed()
		s.mu.Unlock()

		if s.onFrame != nil {
			s.onFrame()
		}

		interval := time.Second / time.Duration(fps)
		if elapsed := time.Since(tStart); elapsed < interval {
			time.Sleep(interval - elapsed)
		}
	}
}

func (s *Server) Close() error {
	if !s.noOpenCV {
		s.frame.mat.Close()
	}
	return s.source.Close()
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
