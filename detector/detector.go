package detector

import (
	"context"
	"image"
	"image/draw"
	"math"
	"runtime"
	"sync"
	"time"

	"gioui.org/layout"

	"github.com/Miuzarte/GoCVStreamer/capturer"
	"github.com/Miuzarte/GoCVStreamer/cuda"
	"github.com/Miuzarte/GoCVStreamer/fps"
	"github.com/Miuzarte/GoCVStreamer/libyuv"
	"github.com/Miuzarte/GoCVStreamer/logger"
	"github.com/Miuzarte/GoCVStreamer/timing"
	"github.com/Miuzarte/GoCVStreamer/ui"
	"github.com/Miuzarte/GoCVStreamer/utils"
	"github.com/getcharzp/go-vision/yolo26"
)

var log = logger.New("Detector")

type Config struct {
	Fps     int
	FpsIdle int

	ModelPath          string
	OnnxLibPath        string
	ConfThresh         float32
	InputSize          int
	UseCuda            bool
	UseTensorRT        bool
	TensorRTPluginPath string

	// https://github.com/ultralytics/ultralytics/blob/main/ultralytics/cfg/datasets/coco.yaml
	ResultIds utils.Set[int]

	CropSize int // 中心裁剪边长：-1=屏幕短边（自动），0=不裁剪，>0=固定值
}

func DefaultConfig() Config {
	return Config{
		Fps:     30,
		FpsIdle: 0,

		ModelPath:          `B:\Git\go-vision\_weights\yolo26_weights\yolo26n.onnx`,
		OnnxLibPath:        `B:\Git\GoCVStreamer\libs\onnxruntime-win-x64-gpu_cuda13-1.28.0\lib\onnxruntime.dll`,
		ConfThresh:         0.45,
		InputSize:          640,
		UseCuda:            false,
		UseTensorRT:        true,
		TensorRTPluginPath: `B:\Lib\TensorRT-RTX-EP-ABI-v0.3.0-cu13\onnxruntime_providers_nv_tensorrt_rtx.dll`,

		ResultIds: utils.NewSet(0),
	}
}

type Stats struct {
	Fps   float64
	Cost  time.Duration
	Count int
}

type Engine struct {
	mu         sync.RWMutex
	fpsCounter fps.Counter

	capturerServer *capturer.Server
	detEngine      *yolo26.DetEngine
	cfg            Config

	// result
	stats         Stats
	personResults []yolo26.DetResult
	personBuf     []yolo26.DetResult

	idleCheck func() bool
	diag      *timing.Diag
}

func New(capturerServer *capturer.Server, cfg Config) (*Engine, error) {
	yCfg := yolo26.Config{
		ModelPath:          cfg.ModelPath,
		OnnxRuntimeLibPath: cfg.OnnxLibPath,
		ConfThreshold:      cfg.ConfThresh,
		InputSize:          cfg.InputSize,
		UseCuda:            cfg.UseCuda,
		UseTensorRT:        cfg.UseTensorRT,
		TensorRTPluginPath: cfg.TensorRTPluginPath,
	}

	detEngine, err := yolo26.NewDetEngine(yCfg)
	if err != nil {
		return nil, err
	}

	return &Engine{
		fpsCounter: fps.NewCounter(time.Second),

		cfg:            cfg,
		capturerServer: capturerServer,
		detEngine:      detEngine,

		diag: timing.NewDiag("Detect"),
	}, nil
}

func (e *Engine) Close() error {
	if e.detEngine != nil {
		e.detEngine.Destroy()
	}
	cuda.DestroyCurrentContext()
	return nil
}

func (e *Engine) Detect(img image.Image) error {
	tStart := time.Now()

	results, err := e.detEngine.Predict(img)
	e.stats.Cost = time.Since(tStart)
	if err != nil {
		return err
	}

	e.mu.Lock()
	e.personBuf = e.personBuf[:0]
	for _, r := range results {
		if e.cfg.ResultIds.Has1(r.ClassID) {
			e.personBuf = append(e.personBuf, r)
		}
	}
	e.personResults = e.personBuf
	e.mu.Unlock()

	return nil
}

// Snapshot 实现 Source 接口：返回本地结果（屏幕坐标系）与最近一次推理延迟。
func (e *Engine) Snapshot() (results []Result, latency time.Duration, fresh bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.personResults) == 0 {
		return nil, e.stats.Cost, false
	}
	results = make([]Result, len(e.personResults))
	for i, d := range e.personResults {
		results[i] = Result{DetResult: d, Kind: KindLocal, Latency: e.stats.Cost}
	}
	return results, e.stats.Cost, true
}

func (e *Engine) Stats() Stats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.stats
}

func (e *Engine) OffsetResults(offset image.Point) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for i := range e.personResults {
		e.personResults[i].Box = e.personResults[i].Box.Add(offset)
	}
}

func (e *Engine) ScaleResults(scaleX, scaleY float64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for i := range e.personResults {
		r := &e.personResults[i]
		r.Box = image.Rect(
			int(float64(r.Box.Min.X)*scaleX),
			int(float64(r.Box.Min.Y)*scaleY),
			int(float64(r.Box.Max.X)*scaleX),
			int(float64(r.Box.Max.Y)*scaleY),
		)
	}
}

func (e *Engine) SetIdleChecker(fn func() bool) {
	e.idleCheck = fn
}

func (e *Engine) Run(ctx context.Context) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	bounds := e.capturerServer.Bounds()
	localImg := image.NewRGBA(bounds)
	var lastFrameId uint64

	cropSize := e.cfg.CropSize
	cropOffset := image.Pt(0, 0)
	cropNeeded := false
	if cropSize < 0 {
		// -1：自动使用屏幕短边（横屏下即屏幕高度），视野最大且保持正方形。
		cropSize = min(bounds.Dx(), bounds.Dy())
	} else if cropSize > 0 {
		cropSize = min(cropSize, bounds.Dx(), bounds.Dy())
	}
	if cropSize > 0 && (cropSize < bounds.Dx() || cropSize < bounds.Dy()) {
		cropNeeded = true
		cropOffset = image.Pt((bounds.Dx()-cropSize)/2, (bounds.Dy()-cropSize)/2)
	}

	cropImg := image.NewRGBA(image.Rect(0, 0, cropSize, cropSize))
	resizeDst := image.NewRGBA(image.Rect(0, 0, e.cfg.InputSize, e.cfg.InputSize))

	interval := time.Second / time.Duration(e.cfg.Fps)
	intervalIdle := time.Duration(math.MaxInt64)
	if e.cfg.FpsIdle != 0 {
		intervalIdle = time.Second / time.Duration(e.cfg.FpsIdle)
	}

	tickerNormal := time.NewTicker(interval)
	defer tickerNormal.Stop()
	tickerIdle := time.NewTicker(intervalIdle)
	defer tickerIdle.Stop()
	mixinTicker := make(chan time.Time, 2)
	defer close(mixinTicker)

	for {
		select {
		case <-ctx.Done():
			return

		case t, ok := <-tickerNormal.C:
			if !ok {
				return
			}
			if e.idleCheck != nil && e.idleCheck() {
				e.mu.Lock()
				e.stats = Stats{}
				e.personResults = nil
				e.mu.Unlock()
				continue
			}
			select {
			case mixinTicker <- t:
			default:
			}
			continue

		case t, ok := <-tickerIdle.C:
			if !ok {
				return
			}
			if e.idleCheck == nil || !e.idleCheck() {
				continue
			}
			select {
			case mixinTicker <- t:
			default:
			}
			continue

		case _, ok := <-mixinTicker:
			if !ok {
				return
			}
		}

		fps := e.cfg.Fps
		if e.idleCheck != nil && e.idleCheck() {
			fps = e.cfg.FpsIdle
		}
		e.capturerServer.RaiseCeiling(fps)

		id := e.capturerServer.ReadFrameId()
		if id == lastFrameId {
			continue
		}
		lastFrameId = id

		captureRgba := e.capturerServer.ReadRgba()
		if captureRgba == nil {
			continue
		}

		var detectImg image.Image
		if cropNeeded {
			draw.Draw(cropImg, cropImg.Bounds(), captureRgba, cropOffset, draw.Src)
			detectImg = cropImg
		} else {
			copy(localImg.Pix, captureRgba.Pix)
			detectImg = localImg
		}
		origW := detectImg.Bounds().Dx()
		origH := detectImg.Bounds().Dy()
		libyuv.ResizeRGBAInto(resizeDst, detectImg.(*image.RGBA), e.cfg.InputSize, e.cfg.InputSize)
		detectImg = resizeDst

		err := e.Detect(detectImg)
		if err != nil {
			log.Warn().
				Err(err).
				Msg("yolo detection failed")
			time.Sleep(time.Millisecond * 100)
			continue
		}
		e.diag.Observe(e.stats.Cost, log)
		if origW != e.cfg.InputSize || origH != e.cfg.InputSize {
			e.ScaleResults(float64(origW)/float64(e.cfg.InputSize), float64(origH)/float64(e.cfg.InputSize))
		}
		if cropNeeded {
			e.OffsetResults(cropOffset)
		}

		e.stats.Fps, _ = e.fpsCounter.Count()
	}
}

func (e *Engine) Draw(gtx layout.Context, s ui.DScale) {
	results, _, fresh := e.Snapshot()
	if !fresh {
		return
	}
	for _, r := range results {
		e.drawDet(gtx, s, r.DetResult, ui.ColorGreen)
	}
}

func (e *Engine) drawDet(gtx layout.Context, s ui.DScale, det yolo26.DetResult, color ui.RGBA) {
	rect := s.Rect(det.Box)
	ui.DrawBorder(gtx, color.NRGBA(), rect)
	labelPos := image.Pt(rect.Min.X, rect.Min.Y-ui.FontSize)
	ui.DrawLabel(gtx, color.NRGBA(), labelPos, ui.FontSize, ui.FormatPct(det.Score))
}
