package detector

import (
	"context"
	"image"
	"runtime"
	"sync"
	"time"

	"gioui.org/layout"

	"github.com/Miuzarte/GoCVStreamer/capturer"
	"github.com/Miuzarte/GoCVStreamer/fps"
	"github.com/Miuzarte/GoCVStreamer/logger"
	"github.com/Miuzarte/GoCVStreamer/ui"
	"github.com/Miuzarte/GoCVStreamer/utils"
	"github.com/getcharzp/go-vision/yolo26"
)

var log = logger.New("Detector")

type Config struct {
	Fps int

	ModelPath          string
	OnnxLibPath        string
	ConfThresh         float32
	InputSize          int
	UseCuda            bool
	UseTensorRT        bool
	TensorRTPluginPath string

	// https://github.com/ultralytics/ultralytics/blob/main/ultralytics/cfg/datasets/coco.yaml
	ResultIds utils.Set[int]
}

func DefaultConfig() Config {
	return Config{
		Fps: 10,

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
	}, nil
}

func (e *Engine) Close() {
	if e.detEngine != nil {
		e.detEngine.Destroy()
	}
}

func (e *Engine) Detect(img image.Image) error {
	tStart := time.Now()

	results, err := e.detEngine.Predict(img)
	e.stats.Cost = time.Since(tStart)
	if err != nil {
		return err
	}

	personResults := make([]yolo26.DetResult, 0, len(results))
	for _, r := range results {
		if e.cfg.ResultIds.Has1(r.ClassID) {
			personResults = append(personResults, r)
		}
	}

	e.mu.Lock()
	e.personResults = personResults
	e.mu.Unlock()

	return nil
}

func (e *Engine) Snapshot() (results []yolo26.DetResult, stats Stats) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	results = make([]yolo26.DetResult, len(e.personResults))
	copy(results, e.personResults)

	return results, e.stats
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

func (e *Engine) Run(ctx context.Context) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	localImg := image.NewRGBA(e.capturerServer.Bounds())
	var lastFrameId uint64

	interval := time.Second / time.Duration(e.cfg.Fps)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		e.capturerServer.Request(capturer.TagYOLO, interval)

		id := e.capturerServer.ReadFrameId()
		if id == lastFrameId {
			continue
		}
		lastFrameId = id

		captureRgba := e.capturerServer.ReadRgba()
		if captureRgba == nil {
			continue
		}
		copy(localImg.Pix, captureRgba.Pix)

		tStart := time.Now()
		err := e.Detect(localImg)
		if err != nil {
			log.Warn().
				Err(err).
				Msg("yolo detection failed")
			continue
		}
		e.stats.Cost = time.Since(tStart)

		e.stats.Fps, _ = e.fpsCounter.Count()
	}
}

func (e *Engine) Draw(gtx layout.Context, s ui.DScale) {
	results, _ := e.Snapshot()
	for _, det := range results {
		rect := s.Rect(det.Box)
		ui.DrawBorder(gtx, ui.ColorGreen.NRGBA(), rect)
		labelPos := image.Pt(rect.Min.X, rect.Min.Y-ui.FontSize)
		ui.DrawLabel(gtx, ui.ColorGreen.NRGBA(), labelPos, ui.FontSize, ui.FormatPct(det.Score))
	}
}
