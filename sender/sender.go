package sender

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"net/http"
	"sync"
	"time"

	"github.com/Miuzarte/GoCVStreamer/capturer"
	"github.com/Miuzarte/GoCVStreamer/fps"
	"github.com/Miuzarte/GoCVStreamer/libyuv"
	"github.com/Miuzarte/GoCVStreamer/logger"
	"github.com/gorilla/websocket"
)

var log = logger.New("Sender")

// RemoteDetection 是手机端回传的归一化检测框（相对 640×640 流帧）。
type RemoteDetection struct {
	X1        float64 `json:"x1"`
	Y1        float64 `json:"y1"`
	X2        float64 `json:"x2"`
	Y2        float64 `json:"y2"`
	Score     float64 `json:"score"`
	Class     int     `json:"class"`
	ClassName string  `json:"class_name"`
}

// RemoteResult 是手机端回传的检测结果 JSON。
type RemoteResult struct {
	FrameID     uint64            `json:"frame_id"`
	Detections  []RemoteDetection `json:"detections"`
	InferenceMs float64           `json:"inference_ms"`
}

type Config struct {
	Addr        string // WebSocket 监听地址，如 ":9090"
	Fps         int    // 推流目标帧率
	JpegQuality int    // JPEG 质量 1-100
	InputSize   int    // 流帧边长（正方形），默认 640
	CropSize    int    // 中心裁剪边长，0 表示不裁剪（默认 1280）
}

type Stats struct {
	Clients       int
	Fps           float64
	FramesSent    uint64
	Detections    uint64
	LastCount     int
	LastAt        time.Time
	LastLatency   time.Duration
	LastInference time.Duration
}

type Server struct {
	cfg Config
	src *capturer.Server

	upgrader websocket.Upgrader

	clientMu sync.Mutex
	clients  map[*websocket.Conn]struct{}

	statsMu  sync.Mutex
	stats    Stats
	fp       fps.Counter
	lastSent uint64

	sentMu sync.Mutex
	sentAt map[uint32]time.Time

	// 裁剪/缩放元数据（runLoop 初始化后只读）
	cropSize   int
	cropOffset image.Point
	cropNeeded bool

	// OnResult 收到手机端检测 JSON 时回调（nil 时只记 stats）。
	// 第二个参数是该帧的全链路延迟（帧发出→收到结果）。
	OnResult func(RemoteResult, time.Duration)
}

func NewServer(cfg Config, src *capturer.Server) *Server {
	if cfg.Fps <= 0 {
		cfg.Fps = 30
	}
	if cfg.JpegQuality <= 0 || cfg.JpegQuality > 100 {
		cfg.JpegQuality = 80
	}
	if cfg.InputSize <= 0 {
		cfg.InputSize = 640
	}
	s := &Server{
		cfg: cfg,
		src: src,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 64 * 1024,
			// 手机 App 无 Origin 限制，全部放行。
			CheckOrigin: func(*http.Request) bool { return true },
		},
		clients: make(map[*websocket.Conn]struct{}),
		sentAt:  make(map[uint32]time.Time),
		fp:      fps.NewCounter(time.Second),
	}

	// 裁剪几何信息在构造时确定（bounds 不变），供 Transform 无锁读取。
	bounds := src.Bounds()
	s.cropSize = cfg.CropSize
	if s.cropSize > 0 {
		s.cropSize = min(s.cropSize, bounds.Dx(), bounds.Dy())
		if s.cropSize < bounds.Dx() || s.cropSize < bounds.Dy() {
			s.cropNeeded = true
			s.cropOffset = image.Pt((bounds.Dx()-s.cropSize)/2, (bounds.Dy()-s.cropSize)/2)
		}
	}
	return s
}

// Run 启动 HTTP + 推流循环，阻塞到 ctx 结束。
func (s *Server) Run(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream", s.handleWS)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "GoCVStreamer WebSocket stream: ws://<host>/stream")
	})

	srv := &http.Server{Addr: s.cfg.Addr, Handler: mux}

	go func() {
		<-ctx.Done()
		s.closeAll()
		srv.Shutdown(context.Background())
	}()
	go s.runLoop(ctx)

	log.Info().
		Str("addr", s.cfg.Addr).
		Int("fps", s.cfg.Fps).
		Int("quality", s.cfg.JpegQuality).
		Int("cropSize", s.cfg.CropSize).
		Msg("stream server started")

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Warn().Err(err).Msg("stream server error")
	}
	<-ctx.Done()
}

// HasClients 是否有手机端在连接（供外部循环决定是否需要推流）。
func (s *Server) HasClients() bool {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	return len(s.clients) != 0
}

func (s *Server) Stats() Stats {
	s.clientMu.Lock()
	clients := len(s.clients)
	s.clientMu.Unlock()
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	s.stats.Clients = clients
	return s.stats
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	c, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	s.clientMu.Lock()
	s.clients[c] = struct{}{}
	s.clientMu.Unlock()

	defer func() {
		s.removeClient(c)
		c.Close()
	}()

	log.Info().
		Str("remote", c.RemoteAddr().String()).
		Msg("stream client connected")

	c.SetReadLimit(1 << 20) // 检测 JSON 足够小，1 MiB 上限
	c.SetReadDeadline(time.Now().Add(60 * time.Second))
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		c.SetReadDeadline(time.Now().Add(60 * time.Second))
		if mt != websocket.TextMessage {
			continue
		}

		var res RemoteResult
		if err := json.Unmarshal(data, &res); err != nil {
			log.Debug().Err(err).Msg("bad result json")
			continue
		}

		latency := time.Duration(0)
		if t, ok := s.frameLatency(uint32(res.FrameID), time.Now()); ok {
			latency = t
		}
		inference := time.Duration(res.InferenceMs * float64(time.Millisecond))

		s.statsMu.Lock()
		s.stats.Detections += uint64(len(res.Detections))
		s.stats.LastCount = len(res.Detections)
		s.stats.LastAt = time.Now()
		s.stats.LastLatency = latency
		s.stats.LastInference = inference
		s.statsMu.Unlock()

		if s.OnResult != nil {
			s.OnResult(res, latency)
		}
	}
}

func (s *Server) removeClient(c *websocket.Conn) {
	s.clientMu.Lock()
	delete(s.clients, c)
	n := len(s.clients)
	s.clientMu.Unlock()
	if n == 0 {
		log.Info().Msg("stream: no clients")
	}
}

func (s *Server) closeAll() {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	for c := range s.clients {
		c.Close()
	}
	s.clients = make(map[*websocket.Conn]struct{})
}

// runLoop 从捕获源拉帧：中心裁剪 → 缩放 640×640 → JPEG → 广播。
func (s *Server) runLoop(ctx context.Context) {
	log.Info().
		Int("cropSize", s.cropSize).
		Bool("cropNeeded", s.cropNeeded).
		Msg("stream frame geometry ready")

	cropImg := image.NewRGBA(image.Rect(0, 0, s.cropSize, s.cropSize))
	resizeDst := image.NewRGBA(image.Rect(0, 0, s.cfg.InputSize, s.cfg.InputSize))

	interval := time.Second / time.Duration(s.cfg.Fps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if !s.HasClients() {
			// 无人观看时不推流，也不抬高捕获帧率（保持最低捕获成本）。
			continue
		}

		// 有客户端时把捕获帧率抬到推流帧率（3 秒窗口，每 tick 续期）。
		s.src.RaiseCeiling(s.cfg.Fps)

		id := s.src.ReadFrameId()
		if id == 0 || id == s.lastSent {
			continue
		}

		rgba := s.src.CloneRgba()
		if rgba == nil {
			continue
		}

		var frame image.Image = rgba
		if s.cropNeeded {
			draw.Draw(cropImg, cropImg.Bounds(), rgba, s.cropOffset, draw.Src)
			frame = cropImg
		}

		libyuv.ResizeRGBAInto(resizeDst, frame.(*image.RGBA), s.cfg.InputSize, s.cfg.InputSize)

		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, resizeDst, &jpeg.Options{Quality: s.cfg.JpegQuality}); err != nil {
			log.Warn().Err(err).Msg("stream: jpeg encode failed")
			continue
		}

		s.lastSent = id
		s.Broadcast(uint32(id), buf.Bytes())
	}
}

// Broadcast 按协议发送 [4B frame_id LE][JPEG]。
func (s *Server) Broadcast(frameID uint32, jpegData []byte) {
	s.clientMu.Lock()

	n := len(s.clients)
	if n == 0 {
		s.clientMu.Unlock()
		return
	}

	msg := make([]byte, 4+len(jpegData))
	binary.LittleEndian.PutUint32(msg, frameID)
	copy(msg[4:], jpegData)

	for c := range s.clients {
		c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := c.WriteMessage(websocket.BinaryMessage, msg); err != nil {
			delete(s.clients, c)
			c.Close()
		}
	}
	s.clientMu.Unlock()

	s.statsMu.Lock()
	s.stats.FramesSent += uint64(n)
	s.stats.Fps, _ = s.fp.Count()
	s.statsMu.Unlock()
	s.recordSent(frameID)
}

func (s *Server) recordSent(frameID uint32) {
	now := time.Now()
	s.sentMu.Lock()
	if len(s.sentAt) > 256 {
		for id, t := range s.sentAt {
			if now.Sub(t) > 2*time.Second {
				delete(s.sentAt, id)
			}
		}
	}
	s.sentAt[frameID] = now
	s.sentMu.Unlock()
}

func (s *Server) frameLatency(frameID uint32, now time.Time) (time.Duration, bool) {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	t, ok := s.sentAt[frameID]
	if !ok {
		return 0, false
	}
	return now.Sub(t), true
}

// Transform 把归一化检测框转换回屏幕坐标（裁剪前全屏坐标系）。
func (s *Server) Transform(d RemoteDetection) image.Rectangle {
	ox := float64(s.cropOffset.X)
	oy := float64(s.cropOffset.Y)
	// 归一化坐标是相对 640×640 流帧的（已除以 InputSize），
	// 直接乘以裁剪区边长 cropSize 即得裁剪区像素坐标，再加偏移到全屏。
	return image.Rect(
		int(d.X1*float64(s.cropSize)+ox+0.5),
		int(d.Y1*float64(s.cropSize)+oy+0.5),
		int(d.X2*float64(s.cropSize)+ox+0.5),
		int(d.Y2*float64(s.cropSize)+oy+0.5),
	)
}
