package yolo

import (
	"image"
	"sync"
	"time"

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
		ModelPath:   "B:\\Git\\go-vision\\_weights\\yolo26_weights\\yolo26n.onnx",
		OnnxLibPath: "B:\\Git\\go-vision\\_weights\\lib\\onnxruntime.dll",
		ConfThresh:  0.45,
		InputSize:   640,
		UseCuda:     false,
	}
}

type Engine struct {
	cfg       Config
	detEngine *yolo26.DetEngine
	log       zerolog.Logger

	mu       sync.RWMutex
	Results  []yolo26.DetResult
	Cost     time.Duration
	DetCount int
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

func (e *Engine) OffsetResults(offset image.Point) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for i := range e.Results {
		e.Results[i].Box = e.Results[i].Box.Add(offset)
	}
}
