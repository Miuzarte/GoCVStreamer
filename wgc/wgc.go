package wgc

import (
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/Miuzarte/GoCVStreamer/capturer"
	"github.com/kirides/go-d3d/outputduplication"
	"github.com/kirides/go-d3d/outputduplication/swizzle"
	"gocv.io/x/gocv"
	"golang.org/x/sys/windows"
)

const (
	wgcOK          = 0
	wgcNoFrame     = 1
	wgcError       = 2
	wgcSizeChanged = 3
	wgcClosed      = 4
)

var (
	loadOnce sync.Once
	loadErr  error
	dll      *windows.DLL

	procSupported     *windows.Proc
	procLastError     *windows.Proc
	procLastHresult   *windows.Proc
	procSetBorderless *windows.Proc
	procCreateMonitor *windows.Proc
	procCreateWindow  *windows.Proc
	procGetWidth      *windows.Proc
	procGetHeight     *windows.Proc
	procGetImage      *windows.Proc
	procGetStats      *windows.Proc
	procClose         *windows.Proc
)

func ensureLoaded() error {
	loadOnce.Do(func() {
		candidates := []string{"wgc_helper.dll"}
		if exe, err := os.Executable(); err == nil {
			candidates = append([]string{filepath.Join(filepath.Dir(exe), "wgc_helper.dll")}, candidates...)
		}
		if wd, err := os.Getwd(); err == nil {
			candidates = append(candidates, filepath.Join(wd, "wgc_helper.dll"))
			// go test 的工作目录是包目录，DLL 通常在项目根。
			candidates = append(candidates, filepath.Join(wd, "..", "wgc_helper.dll"))
		}

		var lastErr error
		for _, c := range candidates {
			var err error
			dll, err = windows.LoadDLL(c)
			if err != nil {
				lastErr = err
				continue
			}
			if procSupported, err = dll.FindProc("wgc_supported"); err == nil {
				procLastError, err = dll.FindProc("wgc_last_error")
			}
			if err == nil {
				procLastHresult, err = dll.FindProc("wgc_last_hresult")
			}
			if err == nil {
				procSetBorderless, err = dll.FindProc("wgc_set_borderless")
			}
			if err == nil {
				procCreateMonitor, err = dll.FindProc("wgc_create_monitor")
			}
			if err == nil {
				procCreateWindow, err = dll.FindProc("wgc_create_window")
			}
			if err == nil {
				procGetWidth, err = dll.FindProc("wgc_get_width")
			}
			if err == nil {
				procGetHeight, err = dll.FindProc("wgc_get_height")
			}
			if err == nil {
				procGetImage, err = dll.FindProc("wgc_get_image")
			}
			if err == nil {
				procGetStats, err = dll.FindProc("wgc_get_stats")
			}
			if err == nil {
				procClose, err = dll.FindProc("wgc_close")
			}
			if err != nil {
				lastErr = fmt.Errorf("wgc_helper.dll missing export: %w", err)
				dll = nil
				continue
			}
			return
		}
		loadErr = fmt.Errorf("load wgc_helper.dll: %w", lastErr)
	})
	return loadErr
}

// PerfStats 为 wgc_helper 的 WgcPerfStats 结构（字段布局必须与 C 侧一致）。
type PerfStats struct {
	SystemQPC      int64
	ArrivedQPC     int64
	ReadyQPC       int64
	TryGetUs       int64
	CopyUs         int64
	MapUs          int64
	RowCopyUs      int64
	FrameID        uint64
	FramesReceived uint64
	FramesReturned uint64
	FramesMissed   uint64
	WaitUs         int64
	CopyOutUs      int64
}

// LastPerf 返回最近一帧的 WGC 阶段耗时与统计；handle 无效时返回 false。
func (s *WgcSource) LastPerf() (PerfStats, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == nil {
		return PerfStats{}, false
	}
	var p PerfStats
	ret, _, _ := procGetStats.Call(uintptr(s.handle), uintptr(unsafe.Pointer(&p)))
	if ret == 0 {
		return PerfStats{}, false
	}
	return p, true
}

// Supported 报告当前系统是否支持 WGC 且 wgc_helper.dll 可加载。
func Supported() bool {
	if err := ensureLoaded(); err != nil {
		return false
	}
	ret, _, _ := procSupported.Call()
	return ret != 0
}

// SetBorderless 控制是否隐藏 WGC 系统绘制的黄色捕获边框（Win10 2004+ 支持）。
func SetBorderless(enabled bool) {
	if err := ensureLoaded(); err != nil {
		return
	}
	v := uintptr(0)
	if enabled {
		v = 1
	}
	procSetBorderless.Call(v)
}

// WgcSource 实现 capturer.Source，通过 wgc_helper.dll 采集显示器或窗口。
type WgcSource struct {
	mu sync.Mutex

	handle unsafe.Pointer
	bounds image.Rectangle

	displayIndex int
	hwnd         windows.HWND
	clientArea   bool
	lookup       func() (windows.HWND, error) // 窗口模式：窗口丢失后重找

	frames     int
	lastReopen time.Time
}

// NewDisplaySource 创建按显示器索引采集的 WGC 源（索引顺序与 DXGI/screenshot 一致）。
func NewDisplaySource(displayIndex int) (*WgcSource, error) {
	if !Supported() {
		return nil, errors.New("WGC unsupported (requires Windows 10 1903+, wgc_helper.dll next to the exe)")
	}
	h, err := createMonitor(displayIndex)
	if err != nil {
		return nil, err
	}
	return &WgcSource{
		handle:       h,
		bounds:       boundsOf(h),
		displayIndex: displayIndex,
	}, nil
}

// NewWindowSource 创建按窗口句柄采集的 WGC 源。
// lookup 在窗口句柄失效后用于重新查找窗口，可为 nil（不自动重找）。
func NewWindowSource(hwnd windows.HWND, clientArea bool, lookup func() (windows.HWND, error)) (*WgcSource, error) {
	if !Supported() {
		return nil, errors.New("WGC unsupported (requires Windows 10 1903+, wgc_helper.dll next to the exe)")
	}
	if hwnd == 0 {
		return nil, errors.New("wgc window source: invalid window handle")
	}
	h, err := createWindow(hwnd, clientArea)
	if err != nil {
		return nil, err
	}
	return &WgcSource{
		handle:     h,
		bounds:     boundsOf(h),
		hwnd:       hwnd,
		clientArea: clientArea,
		lookup:     lookup,
	}, nil
}

func createMonitor(index int) (unsafe.Pointer, error) {
	ret, _, _ := procCreateMonitor.Call(uintptr(index))
	if ret == 0 {
		return nil, fmt.Errorf("wgc_create_monitor(%d) failed: %s", index, lastErrorDetail())
	}
	return unsafe.Pointer(ret), nil
}

func createWindow(hwnd windows.HWND, clientArea bool) (unsafe.Pointer, error) {
	area := uintptr(0)
	if clientArea {
		area = 1
	}
	ret, _, _ := procCreateWindow.Call(uintptr(hwnd), area)
	if ret == 0 {
		return nil, fmt.Errorf("wgc_create_window(0x%X) failed: %s", hwnd, lastErrorDetail())
	}
	return unsafe.Pointer(ret), nil
}

func lastErrorDetail() string {
	stage, _, _ := procLastError.Call()
	hr, _, _ := procLastHresult.Call()
	return fmt.Sprintf("stage=%d hresult=0x%08X", uint32(stage), uint32(int32(hr)))
}

func boundsOf(h unsafe.Pointer) image.Rectangle {
	w, hh := sizeOf(h)
	return image.Rect(0, 0, int(w), int(hh))
}

func sizeOf(h unsafe.Pointer) (uint32, uint32) {
	if h == nil {
		return 0, 0
	}
	w, _, _ := procGetWidth.Call(uintptr(h))
	hh, _, _ := procGetHeight.Call(uintptr(h))
	return uint32(w), uint32(hh)
}

// Bounds 返回当前采集画面尺寸（首帧到达后为实际尺寸）。
func (s *WgcSource) Bounds() image.Rectangle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bounds
}

func (s *WgcSource) GetImage(img *image.RGBA) error {
	return s.GetImageTimeout(img, 10)
}

func (s *WgcSource) GetImageTimeout(img *image.RGBA, timeoutMs uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.handle == nil {
		return errors.New("wgc source closed")
	}
	var dstPtr unsafe.Pointer
	if len(img.Pix) > 0 {
		dstPtr = unsafe.Pointer(&img.Pix[0])
	}

	ret, _, _ := procGetImage.Call(
		uintptr(s.handle),
		uintptr(dstPtr),
		uintptr(len(img.Pix)),
		uintptr(timeoutMs),
	)

	switch ret {
	case wgcOK:
		swizzle.BGRA(img.Pix)
		s.frames++
		return nil
	case wgcNoFrame:
		return outputduplication.ErrNoImageYet
	case wgcSizeChanged:
		s.bounds = boundsOf(s.handle)
		return capturer.ErrSizeChanged
	case wgcClosed:
		if err := s.reopenLocked(); err != nil {
			return err
		}
		return outputduplication.ErrNoImageYet
	default:
		return fmt.Errorf("wgc_get_image: unexpected result %d", ret)
	}
}

// reopenLocked 在源失效后重建：窗口模式重找窗口，显示器模式按原索引重开。
func (s *WgcSource) reopenLocked() error {
	if time.Since(s.lastReopen) < time.Second {
		return errors.New("wgc capture closed, reopen throttled")
	}
	s.lastReopen = time.Now()

	s.closeLocked()

	var h unsafe.Pointer
	var err error
	if s.lookup != nil {
		hwnd, e := s.lookup()
		if e != nil {
			return fmt.Errorf("wgc window lookup failed: %w", e)
		}
		s.hwnd = hwnd
		h, err = createWindow(hwnd, s.clientArea)
	} else {
		h, err = createMonitor(s.displayIndex)
	}
	if err != nil {
		return err
	}
	s.handle = h
	s.bounds = boundsOf(h)
	return nil
}

func (s *WgcSource) ProvideMat(dst *gocv.Mat) bool {
	return false
}

func (s *WgcSource) FramesElapsed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frames
}

func (s *WgcSource) ResetFramesElapsed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = 0
}

func (s *WgcSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
	return nil
}

func (s *WgcSource) closeLocked() {
	if s.handle != nil {
		procClose.Call(uintptr(s.handle))
		s.handle = nil
	}
}
