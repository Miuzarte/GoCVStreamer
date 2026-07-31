package detector

import (
	"image"
	"sync"
	"time"

	"gioui.org/layout"

	"github.com/Miuzarte/GoCVStreamer/ui"
	"github.com/getcharzp/go-vision/yolo26"
)

// Kind 推理结果来源。
type Kind int

const (
	KindLocal Kind = iota
	KindRemote
)

// Result 带来源与延迟的检测结果。
// Latency：本地=推理耗时；远程=帧发出到收到结果的全链路延迟（含网络+手机推理）。
type Result struct {
	yolo26.DetResult
	Kind    Kind
	Latency time.Duration
}

// Source 推理源接口（类比 capturer.Source：可以是本地 YOLO、远程 NPU 等）。
type Source interface {
	// Snapshot 返回当前结果（拷贝）、该批结果对应延迟与是否新鲜。
	Snapshot() (results []Result, latency time.Duration, fresh bool)
	Close() error
}

// RemoteSource 远程（手机端 NPU）推理源：结果由 WebSocket 回调写入。
type RemoteSource struct {
	ttl time.Duration

	mu      sync.RWMutex
	results []Result
	latency time.Duration
	recv    time.Time
}

func NewRemoteSource(ttl time.Duration) *RemoteSource {
	if ttl < 0 {
		ttl = 500 * time.Millisecond
	}
	return &RemoteSource{ttl: ttl}
}

// SetResults 由远程回调写入（屏幕坐标系）；latency 为该帧全链路延迟。
func (s *RemoteSource) SetResults(dets []yolo26.DetResult, latency time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = s.results[:0]
	for _, d := range dets {
		s.results = append(s.results, Result{DetResult: d, Kind: KindRemote, Latency: latency})
	}
	s.latency = latency
	s.recv = time.Now()
}

func (s *RemoteSource) Snapshot() ([]Result, time.Duration, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if time.Since(s.recv) > s.ttl {
		return nil, s.latency, false
	}
	return append([]Result(nil), s.results...), s.latency, true
}

func (s *RemoteSource) Close() error { return nil }

// Drawer 聚合绘制多个推理源：本地绿框、远程青框。
type Drawer struct {
	Sources []Source
}

func (d *Drawer) Draw(gtx layout.Context, s ui.DScale) {
	for _, src := range d.Sources {
		results, _, fresh := src.Snapshot()
		if !fresh {
			continue
		}
		for _, r := range results {
			color := ui.ColorGreen
			if r.Kind == KindRemote {
				color = ui.ColorCyan
			}
			rect := s.Rect(r.Box)
			ui.DrawBorder(gtx, color.NRGBA(), rect)
			labelPos := image.Pt(rect.Min.X, rect.Min.Y-ui.FontSize)
			ui.DrawLabel(gtx, color.NRGBA(), labelPos, ui.FontSize, ui.FormatPct(r.Score))
		}
	}
}
