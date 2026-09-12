package assist

import (
	"image"
	"sync"
	"testing"
	"time"

	"github.com/Miuzarte/GoCVStreamer/detector"
	"github.com/Miuzarte/GoCVStreamer/mouse"
	"github.com/getcharzp/go-vision/yolo26"
)

// fakeSource 是固定内容的检测源
type fakeSource struct {
	mu      sync.Mutex
	results []detector.Result
	latency time.Duration
	fresh   bool
}

func (s *fakeSource) Set(results []detector.Result, latency time.Duration, fresh bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = results
	s.latency = latency
	s.fresh = fresh
}

func (s *fakeSource) Snapshot() ([]detector.Result, time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]detector.Result(nil), s.results...), s.latency, s.fresh
}

func (s *fakeSource) Close() error { return nil }

// fakeMover 记录注入的位移
type fakeMover struct {
	mu    sync.Mutex
	moves [][2]int
}

func (m *fakeMover) record(dx, dy int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.moves = append(m.moves, [2]int{dx, dy})
}

func (m *fakeMover) Move(dx, dy int) error        { m.record(dx, dy); return nil }
func (m *fakeMover) MoveAndMark(dx, dy int) error { m.record(dx, dy); return nil }
func (m *fakeMover) MouseDown(button int) error   { return nil }
func (m *fakeMover) MouseUp(button int) error     { return nil }
func (m *fakeMover) MouseClick(button int) error  { return nil }

func (m *fakeMover) Moves() [][2]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][2]int(nil), m.moves...)
}

var _ mouse.Mover = (*fakeMover)(nil)

// clock 是可控时钟
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// 屏幕 1000x1000, 中心 (500, 500)
var testBounds = image.Rect(0, 0, 1000, 1000)

func testConfig(maxAge time.Duration) Config {
	return Config{
		Enabled:    true,
		Horizontal: true,
		Vertical:   false,
		Speed:      8,
		InnerRatio: 0.5,
		MaxAge:     maxAge,
	}
}

// nearBox 是"在捕获半径内且需要向右修正"的框: 左边界 510 落在 stopL(535) 左边
func nearBox() image.Rectangle { return image.Rect(510, 480, 610, 580) }

// farLeftBox 是"需要向左修正"的框
func farLeftBox() image.Rectangle { return image.Rect(390, 480, 490, 580) }

// centeredBox 是准星落在内缩死区内的框
func centeredBox() image.Rectangle { return image.Rect(400, 400, 600, 600) }

// result 构造一条本地结果; at / publishedAt 为绝对时刻 (零值表示未打戳)
func result(box image.Rectangle, at, publishedAt time.Time) detector.Result {
	return detector.Result{
		DetResult:   yolo26.DetResult{ClassID: 0, Score: 0.9, Box: box},
		Kind:        detector.KindLocal,
		At:          at,
		PublishedAt: publishedAt,
	}
}

func newEngine(t *testing.T, cfg Config, clk *clock, sources ...detector.Source) (*Engine, *fakeMover) {
	t.Helper()
	mover := &fakeMover{}
	e := New(cfg, sources, testBounds, mover)
	e.now = clk.now
	return e, mover
}

// TestFreshResultMoves 结果寿命在限内时正常注入, 单次不超过 Speed
func TestFreshResultMoves(t *testing.T) {
	now := time.Now()
	clk := &clock{t: now}
	src := &fakeSource{}
	src.Set([]detector.Result{result(nearBox(), now.Add(-20*time.Millisecond), now.Add(-10*time.Millisecond))}, 8*time.Millisecond, true)

	e, mover := newEngine(t, testConfig(33*time.Millisecond), clk, src)
	e.Tick()

	moves := mover.Moves()
	if len(moves) != 1 {
		t.Fatalf("want 1 move, got %d (%v)", len(moves), moves)
	}
	if moves[0] != [2]int{8, 0} {
		t.Fatalf("want move {8 0}, got %v", moves[0])
	}
	if gated := e.gatedCount.Load(); gated != 0 {
		t.Fatalf("want gated 0, got %d", gated)
	}
}

// TestStaleResultGated 结果寿命超过上限时一次都不注入, 并且计数
func TestStaleResultGated(t *testing.T) {
	now := time.Now()
	clk := &clock{t: now}
	src := &fakeSource{}
	src.Set([]detector.Result{result(nearBox(), now.Add(-50*time.Millisecond), now.Add(-40*time.Millisecond))}, 8*time.Millisecond, true)

	e, mover := newEngine(t, testConfig(33*time.Millisecond), clk, src)
	e.Tick()

	if moves := mover.Moves(); len(moves) != 0 {
		t.Fatalf("want no move, got %v", moves)
	}
	if gated := e.gatedCount.Load(); gated != 1 {
		t.Fatalf("want gated 1, got %d", gated)
	}

	age, gated := e.Status()
	if age != 40*time.Millisecond || gated != 1 {
		t.Fatalf("want Status(40ms, 1), got (%v, %d)", age, gated)
	}
}

// TestZeroMaxAgeNeverGates MaxAge=0 时退回旧行为 (不门控)
func TestZeroMaxAgeNeverGates(t *testing.T) {
	now := time.Now()
	clk := &clock{t: now}
	src := &fakeSource{}
	src.Set([]detector.Result{result(nearBox(), now.Add(-50*time.Millisecond), now.Add(-40*time.Millisecond))}, 8*time.Millisecond, true)

	e, mover := newEngine(t, testConfig(0), clk, src)
	e.Tick()

	if moves := mover.Moves(); len(moves) != 1 {
		t.Fatalf("want 1 move, got %v", moves)
	}
	if gated := e.gatedCount.Load(); gated != 0 {
		t.Fatalf("want gated 0, got %d", gated)
	}
}

// TestActuationWindowBoundedByMaxAge 核心修复: 同一个结果只会驱动 MaxAge 时长的注入
//
// 旧实现里同一个结果会被 125Hz 的 tick 反复注入, 检测 30FPS -> 15FPS 时注入次数翻倍,
// 且一直朝 66ms 前的位置拉; 现在注入窗口固定为 MaxAge, 与检测率无关
func TestActuationWindowBoundedByMaxAge(t *testing.T) {
	const (
		tick    = 8 * time.Millisecond
		maxAge  = 33 * time.Millisecond
		ticks   = 13 // 覆盖 0..96ms
		allowed = 5  // 0, 8, 16, 24, 32ms
	)
	base := time.Now()
	clk := &clock{t: base}
	src := &fakeSource{}
	src.Set([]detector.Result{result(nearBox(), base, base)}, 8*time.Millisecond, true)

	e, mover := newEngine(t, testConfig(maxAge), clk, src)
	for i := 0; i < ticks; i++ {
		clk.t = base.Add(time.Duration(i) * tick)
		e.Tick()
	}

	if moves := mover.Moves(); len(moves) != allowed {
		t.Fatalf("want %d moves within the window, got %d (%v)", allowed, len(moves), moves)
	}
	if gated := e.gatedCount.Load(); gated != uint64(ticks-allowed) {
		t.Fatalf("want gated %d, got %d", ticks-allowed, gated)
	}
}

// TestPickFreshestSource 按信息年龄选源, 不是按推理耗时
func TestPickFreshestSource(t *testing.T) {
	now := time.Now()
	clk := &clock{t: now}

	// 本地: 信息年龄 60ms, 但推理只要 2ms -> 需要向右修正
	local := &fakeSource{}
	local.Set([]detector.Result{result(nearBox(), now.Add(-60*time.Millisecond), now.Add(-58*time.Millisecond))}, 2*time.Millisecond, true)

	// 远端: 信息年龄 20ms, 但全链路延迟 80ms -> 需要向左修正
	remote := &fakeSource{}
	remote.Set([]detector.Result{result(farLeftBox(), now.Add(-20*time.Millisecond), now)}, 80*time.Millisecond, true)

	e, mover := newEngine(t, testConfig(200*time.Millisecond), clk, local, remote)
	e.Tick()

	moves := mover.Moves()
	if len(moves) != 1 {
		t.Fatalf("want 1 move, got %v", moves)
	}
	if moves[0] != [2]int{-8, 0} {
		t.Fatalf("want the fresher remote source to win (move {-8 0}), got %v", moves[0])
	}
}

// TestInsideDeadbandNoMove 准星已在死区内时不注入
func TestInsideDeadbandNoMove(t *testing.T) {
	now := time.Now()
	clk := &clock{t: now}
	src := &fakeSource{}
	src.Set([]detector.Result{result(centeredBox(), now, now)}, 8*time.Millisecond, true)

	e, mover := newEngine(t, testConfig(33*time.Millisecond), clk, src)
	e.Tick()

	if moves := mover.Moves(); len(moves) != 0 {
		t.Fatalf("want no move, got %v", moves)
	}
}

// TestNoResultsNoMove 无结果 / 不新鲜时不注入
func TestNoResultsNoMove(t *testing.T) {
	now := time.Now()
	clk := &clock{t: now}
	src := &fakeSource{}
	src.Set(nil, 8*time.Millisecond, false)

	e, mover := newEngine(t, testConfig(33*time.Millisecond), clk, src)
	e.Tick()

	if moves := mover.Moves(); len(moves) != 0 {
		t.Fatalf("want no move, got %v", moves)
	}
}

// TestMissingTimestampFallsBackToLatency 未打戳时用推理耗时当作结果寿命
func TestMissingTimestampFallsBackToLatency(t *testing.T) {
	now := time.Now()
	clk := &clock{t: now}
	src := &fakeSource{}
	src.Set([]detector.Result{result(nearBox(), time.Time{}, time.Time{})}, 100*time.Millisecond, true)

	e, mover := newEngine(t, testConfig(33*time.Millisecond), clk, src)
	e.Tick()

	if moves := mover.Moves(); len(moves) != 0 {
		t.Fatalf("want no move (latency 100ms > maxAge), got %v", moves)
	}
	if gated := e.gatedCount.Load(); gated != 1 {
		t.Fatalf("want gated 1, got %d", gated)
	}
}
