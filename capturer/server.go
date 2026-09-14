package capturer

import (
	"context"
	"errors"
	"image"
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
	// MatchRoi 是模板匹配读取的兴趣区。灰度转换只对这块做, 不再整帧转换
	// 零值 = 整帧 (退化为旧行为)
	MatchRoi image.Rectangle
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
	// at 为该帧的采集完成时刻, 供消费方计算帧龄 (capture -> now)
	at time.Time
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

	// frameCh 用于通知帧驱动型消费方 (如检测循环) 有新帧, 容量 1 丢旧留新
	frameCh chan struct{}

	targetFps    int
	targetExpiry time.Time
	targetMu     sync.Mutex

	noOpenCV bool

	// matchRoi 是当前灰度化 + 模板匹配的兴趣区, roiRGBA 是它对应的 RGBA 中转 Mat
	// 只对 ROI 做拷贝与色彩转换, 避免为 88x104 的窗口每帧搬整张 2560x1440
	matchRoi image.Rectangle
	roiRGBA  gocv.Mat

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
		frameCh:    make(chan struct{}, 1),
		noOpenCV:   cfg.DisableOpenCV,

		diagGetImage:   timing.NewDiag("GetImage"),
		diagImageToMat: timing.NewDiag("ImageToMat"),
	}
	s.matchRoi = clampRoi(cfg.MatchRoi, bounds)
	if !cfg.DisableOpenCV {
		s.frame.mat = gocv.NewMat()
		s.roiRGBA = gocv.NewMatWithSize(s.matchRoi.Dy(), s.matchRoi.Dx(), gocv.MatTypeCV8UC4)
	}
	return s
}

// clampRoi 把兴趣区夹到画面内; 空值或完全出界时退化为整帧
func clampRoi(roi, bounds image.Rectangle) image.Rectangle {
	if roi.Empty() {
		return bounds
	}
	r := roi.Intersect(bounds)
	if r.Empty() {
		return bounds
	}
	return r
}

// SetMatchRoi 更新模板匹配的兴趣区并重建 ROI 缓冲 (快捷键调整 ROI 后调用)
func (s *Server) SetMatchRoi(roi image.Rectangle) {
	if s.noOpenCV {
		return
	}
	r := clampRoi(roi, s.source.Bounds())

	s.mu.Lock()
	defer s.mu.Unlock()
	if r == s.matchRoi {
		return
	}
	s.matchRoi = r
	s.roiRGBA.Close()
	s.roiRGBA = gocv.NewMatWithSize(r.Dy(), r.Dx(), gocv.MatTypeCV8UC4)
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

// ReadFrame 一次返回最新帧的 RGBA 数据 / 帧号 / 采集时刻
//
// 三者必须同临界区取, 否则消费方可能把新旧帧配到一起
func (s *Server) ReadFrame() (*image.RGBA, uint64, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.frame.rgba, s.frame.id, s.frame.at
}

// FrameCh 返回新帧通知通道 (容量 1, 丢旧留新), 供帧驱动型消费方 select
func (s *Server) FrameCh() <-chan struct{} {
	return s.frameCh
}

// CloneRgba 返回最新 RGBA 帧的深拷贝 (供其他 goroutine 编码/发送用)
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

// CloneMat 返回最新 OpenCV Mat 的深拷贝 (供其他 goroutine 编码/发送用)
// 第二个返回值表示是否可用 (noopencv 模式下为空)
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

	log.Info().
		Int("width", s.source.Bounds().Dx()).
		Int("height", s.source.Bounds().Dy()).
		Msg("capture server started")

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
		if errors.Is(err, outputduplication.ErrNoImageYet) {
			continue
		}
		if errors.Is(err, ErrSizeChanged) {
			log.Info().
				Msg("capture size changed, rebuilding frame buffers")
			s.reallocBuffers(&rawRGBA)
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
		s.frame.at = time.Now()

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

		// 通知帧驱动型消费方 (容量 1, 非阻塞: 旧通知没被取走就丢弃, 消费方总是读最新帧)
		select {
		case s.frameCh <- struct{}{}:
		default:
		}

		if elapsed := time.Since(tStart); elapsed < interval {
			time.Sleep(interval - elapsed)
		}
	}
}

// reallocBuffers 按采集源当前尺寸重建帧缓冲 (分辨率变化时调用)
func (s *Server) reallocBuffers(rawRGBA **image.RGBA) {
	bounds := s.source.Bounds()
	s.mu.Lock()
	defer s.mu.Unlock()

	*rawRGBA = image.NewRGBA(bounds)
	s.screenRGBA = image.NewRGBA(bounds)
	if !s.noOpenCV {
		s.matchRoi = clampRoi(s.cfg.MatchRoi, bounds)
		s.roiRGBA.Close()
		s.roiRGBA = gocv.NewMatWithSize(s.matchRoi.Dy(), s.matchRoi.Dx(), gocv.MatTypeCV8UC4)
	}
	log.Info().
		Int("width", bounds.Dx()).
		Int("height", bounds.Dy()).
		Msg("frame buffers rebuilt")
}

func (s *Server) Close() error {
	if !s.noOpenCV {
		s.frame.mat.Close()
		s.roiRGBA.Close()
	}
	return s.source.Close()
}

// imageToMat 把 MatchRoi 区域转成灰度 (或 BGR) 写进 dst
//
// 只处理 ROI: 模板匹配只读这一小块 (默认 88x104), 原来却对整张 2560x1440 做
// 14.7MB 拷贝 + CvtColor, 每帧白搬约 33MB
func (s *Server) imageToMat(img image.Image, dst *gocv.Mat) (err error) {
	roi := s.matchRoi

	if m, ok := img.(*image.RGBA); ok {
		data, err := s.roiRGBA.DataPtrUint8()
		if err != nil {
			return err
		}
		rowBytes := roi.Dx() * 4
		for y := range roi.Dy() {
			srcOff := m.PixOffset(roi.Min.X, roi.Min.Y+y)
			copy(data[y*rowBytes:(y+1)*rowBytes], m.Pix[srcOff:srcOff+rowBytes])
		}
		return gocv.CvtColor(s.roiRGBA, dst, s.cvtCode)
	}

	imageToMatWarnOnce.Do(func() {
		log.Warn().Msg("unexpected image color model, conversion performance may be affected")
	})

	gray := s.cvtCode == gocv.ColorRGBAToGray
	channels := 3
	if gray {
		channels = 1
	}

	data := make([]byte, 0, roi.Dx()*roi.Dy()*channels)
	for j := roi.Min.Y; j < roi.Max.Y; j++ {
		for i := roi.Min.X; i < roi.Max.X; i++ {
			r, g, b, _ := img.At(i, j).RGBA()
			if gray {
				data = append(data, byte((19595*uint32(r)+38470*uint32(g)+7471*uint32(b))>>16))
				continue
			}
			data = append(data, byte(b>>8), byte(g>>8), byte(r>>8))
		}
	}

	mt := gocv.MatTypeCV8UC3
	if gray {
		mt = gocv.MatTypeCV8UC1
	}
	src, err := gocv.NewMatFromBytes(roi.Dy(), roi.Dx(), mt, data)
	if err != nil {
		return err
	}
	defer src.Close()
	src.CopyTo(dst)
	return nil
}
