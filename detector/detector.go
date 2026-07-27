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
	"github.com/Miuzarte/GoCVStreamer/ui"
	"github.com/getcharzp/go-vision/yolo26"
	"github.com/rs/zerolog"
)

type Config struct {
	ModelPath          string
	OnnxLibPath        string
	ConfThresh         float32
	InputSize          int
	UseCuda            bool
	UseTensorRT        bool
	TensorRTPluginPath string
}

func DefaultConfig() Config {
	return Config{
		ModelPath:          `B:\Git\go-vision\_weights\yolo26_weights\yolo26n.onnx`,
		OnnxLibPath:        `B:\Git\GoCVStreamer\libs\onnxruntime-win-x64-gpu_cuda13-1.28.0\lib\onnxruntime.dll`,
		ConfThresh:         0.25,
		InputSize:          640,
		UseCuda:            false,
		UseTensorRT:        true,
		TensorRTPluginPath: `B:\Lib\TensorRT-RTX-EP-ABI-v0.3.0-cu13\onnxruntime_providers_nv_tensorrt_rtx.dll`,
	}
}

type Stats struct {
	FPS    float64
	CostMs float64
	Count  int
}

type Engine struct {
	cfg       Config
	detEngine *yolo26.DetEngine
	log       zerolog.Logger

	fp       fps.Counter
	mu       sync.RWMutex
	Results  []yolo26.DetResult
	Cost     time.Duration
	DetCount int
	lastFPS  float64
}

func New(cfg Config, log zerolog.Logger) (*Engine, error) {
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
		cfg:       cfg,
		detEngine: detEngine,
		log:       log,
		fp:        fps.NewCounter(time.Second),
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
	e.Cost = time.Since(tStart)
	if err != nil {
		return err
	}

	persons := make([]yolo26.DetResult, 0, len(results))
	for _, r := range results {
		if r.ClassID == 0 {
			persons = append(persons, r)
		}
	}

	e.mu.Lock()
	e.Results = persons
	e.DetCount = len(persons)
	e.mu.Unlock()

	return nil
}

func (e *Engine) Snapshot() (results []yolo26.DetResult, count int, cost time.Duration) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	results = make([]yolo26.DetResult, len(e.Results))
	copy(results, e.Results)

	return results, e.DetCount, e.Cost
}

func (e *Engine) Stats() Stats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return Stats{
		FPS:    e.lastFPS,
		CostMs: float64(e.Cost) / float64(time.Millisecond),
		Count:  e.DetCount,
	}
}

func (e *Engine) OffsetResults(offset image.Point) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for i := range e.Results {
		e.Results[i].Box = e.Results[i].Box.Add(offset)
	}
}

func (e *Engine) Run(ctx context.Context, capSrv *capturer.Server) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	const interval = time.Second / 15
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	localImg := image.NewRGBA(capSrv.Bounds())
	var lastFrameID uint64
	var lastFPS float64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		capSrv.Request(capturer.TagYOLO, interval)

		id := capSrv.ReadFrameID()
		if id == lastFrameID {
			continue
		}
		lastFrameID = id

		srcRGBA := capSrv.ReadRGBA()
		if srcRGBA == nil {
			continue
		}
		copy(localImg.Pix, srcRGBA.Pix)

		tStart := time.Now()
		err := e.Detect(localImg)
		if err != nil {
			e.log.Warn().Err(err).Msg("person detection failed")
			continue
		}
		e.Cost = time.Since(tStart)

		lastFPS, _ = e.fp.Count()

		e.mu.Lock()
		e.lastFPS = lastFPS
		e.mu.Unlock()
	}
}

func (e *Engine) Draw(gtx layout.Context, s ui.DScale) {
	results, _, _ := e.Snapshot()
	for _, det := range results {
		rect := s.Rect(det.Box)
		ui.DrawBorder(gtx, ui.ColorGreen.NRGBA(), rect)
		labelPos := image.Pt(rect.Min.X, rect.Min.Y-ui.FontSize)
		ui.DrawLabel(gtx, ui.ColorGreen.NRGBA(), labelPos, ui.FontSize, ui.FormatPct(det.Score))
	}
}
