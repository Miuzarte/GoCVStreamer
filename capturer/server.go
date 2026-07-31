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

	frameMatRGBAInter gocv.Mat

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
		s.frameMatRGBAInter = gocv.NewMatWithSize(bounds.Dy(), bounds.Dx(), gocv.MatTypeCV8UC4)
	}
	return s
}

func (s *Server) Bounds() image.Rectangle {
	return s.source.Bounds()
}

func (s *Server) RaiseCeiling(fps int) {
	if fps <= 0 {
		return
	}
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

// CloneRgba 返回最新 RGBA 帧的深拷贝（供其他 goroutine 编码/发送用）。
func (s *Server) CloneRgba() *image.RGBA {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.frame.rgba == nil {
		return nil
	}
	cp := image.NewRGBA(s.frame.rgba.Bounds())
	copy(cp.Pix, s.frame.rgba.Pix)
	return cp
}

func (s *Server) ReadMat() gocv.Mat {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.frame.mat
}

// CloneMat 返回最新 OpenCV Mat 的深拷贝（供其他 goroutine 编码/发送用）。
// 第二个返回值表示是否可用（noopencv 模式下为空）。
func (s *Server) CloneMat() (gocv.Mat, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.frame.mat.Empty() {
		return gocv.Mat{}, false
	}
	return s.frame.mat.Clone(), true
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

		interval := time.Second / time.Duration(fps)
		timeoutMs := max(1, interval.Milliseconds())

		tStart := time.Now()

		err := s.source.GetImageTimeout(rawRGBA, uint(timeoutMs))
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

		s.screenRGBA, rawRGBA = rawRGBA, s.screenRGBA

		s.stats.Cost = time.Since(tStart)
		s.stats.FPS, s.stats.FrameTime = s.fp.Count()
		s.stats.FrameCount = s.source.FramesElapsed()
		s.mu.Unlock()

		if s.onFrame != nil {
			s.onFrame()
		}

		if elapsed := time.Since(tStart); elapsed < interval {
			time.Sleep(interval - elapsed)
		}
	}
}

func (s *Server) Close() error {
	if !s.noOpenCV {
		s.frame.mat.Close()
		s.frameMatRGBAInter.Close()
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
		data, err := s.frameMatRGBAInter.DataPtrUint8()
		if err != nil {
			return err
		}
		copy(data, m.Pix)
		return gocv.CvtColor(s.frameMatRGBAInter, dst, s.cvtCode)

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
