package sender_test

import (
	"context"
	"encoding/binary"
	jsonv2 "encoding/json/v2"
	"image"
	"image/color"
	"image/draw"
	"net"
	"testing"
	"time"

	"github.com/Miuzarte/GoCVStreamer/capturer"
	"github.com/Miuzarte/GoCVStreamer/sender"
	"github.com/coder/websocket"
	"gocv.io/x/gocv"
)

// fakeSource 是 capturer.Source 的最小实现：输出固定纯色帧。
type fakeSource struct {
	bounds image.Rectangle
}

func (f *fakeSource) Bounds() image.Rectangle { return f.bounds }

func (f *fakeSource) GetImage(img *image.RGBA) error {
	draw.Draw(img, img.Bounds(),
		image.NewUniform(color.RGBA{R: 40, G: 80, B: 120, A: 255}), image.Point{}, draw.Src)
	return nil
}

func (f *fakeSource) GetImageTimeout(img *image.RGBA, _ uint) error {
	return f.GetImage(img)
}

func (f *fakeSource) ProvideMat(*gocv.Mat) bool { return false }
func (f *fakeSource) FramesElapsed() int        { return 0 }
func (f *fakeSource) ResetFramesElapsed()       {}
func (f *fakeSource) Close() error              { return nil }

// TestWebSocketLifecycle 覆盖 sender 的完整生命周期：
// 握手 → 推流 → 回传结果 → 客户端断开清理 → ctx 取消后 Run 退出并释放端口。
func TestWebSocketLifecycle(t *testing.T) {
	const addr = "127.0.0.1:19091"

	capSrv := capturer.NewServer(
		&fakeSource{bounds: image.Rect(0, 0, 1280, 720)},
		capturer.Config{MinFps: 30, DisableOpenCV: true},
		0,
		nil,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go capSrv.Run(ctx)
	defer capSrv.Close()

	srv := sender.NewServer(sender.Config{
		Addr:        addr,
		Fps:         30,
		JpegQuality: 80,
		InputSize:   640,
	}, capSrv)

	resultCh := make(chan sender.RemoteResult, 1)
	srv.OnResult = func(res sender.RemoteResult, _ time.Duration) {
		resultCh <- res
	}

	done := make(chan struct{})
	go func() {
		srv.Run(ctx)
		close(done)
	}()

	// 1. 等端口就绪并建立连接
	var c *websocket.Conn
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		c, _, err = websocket.Dial(context.Background(), "ws://"+addr+"/stream", nil)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer c.CloseNow()

	// 2. 收到推流帧（[4B frame_id LE][JPEG]）
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	mt, data, err := c.Read(readCtx)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if mt != websocket.MessageBinary || len(data) < 5 {
		t.Fatalf("bad frame: mt=%v len=%d", mt, len(data))
	}
	frameID := binary.LittleEndian.Uint32(data[:4])

	// 3. 回传检测 JSON，OnResult 应被调用
	res := sender.RemoteResult{
		FrameID: uint64(frameID),
		Detections: []sender.RemoteDetection{{
			X1: 0.1, Y1: 0.2, X2: 0.5, Y2: 0.6,
			Score: 0.9, Class: 0, ClassName: "person",
		}},
		InferenceMs: 18.5,
	}
	payload, _ := jsonv2.Marshal(res)
	writeCtx, writeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer writeCancel()
	if err := c.Write(writeCtx, websocket.MessageText, payload); err != nil {
		t.Fatalf("write result: %v", err)
	}

	select {
	case got := <-resultCh:
		if got.FrameID != uint64(frameID) || len(got.Detections) != 1 {
			t.Fatalf("bad OnResult: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnResult not called")
	}
	if !srv.HasClients() {
		t.Fatal("HasClients should be true while connected")
	}

	// 4. 客户端断开后，服务端应清理连接
	c.CloseNow()
	deadline = time.Now().Add(5 * time.Second)
	for srv.HasClients() {
		if time.Now().After(deadline) {
			t.Fatal("client not removed after disconnect")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 5. 取消 ctx：Run 应退出，端口应释放
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit after cancel")
	}
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("port still listening after shutdown")
	}
}
