package capturer

import (
	"errors"
	"fmt"
	"image"
	"sync"

	"github.com/Miuzarte/GoCVStreamer/logger"
	"github.com/kbinani/screenshot"
	"github.com/kirides/go-d3d/d3d11"
	"github.com/kirides/go-d3d/outputduplication"
	"github.com/kirides/go-d3d/win"
	"gocv.io/x/gocv"
)

var log = logger.New("Capturer")

// ErrSizeChanged 表示采集源的分辨率发生变化，调用方应重建帧缓冲。
var ErrSizeChanged = errors.New("capture source size changed")

type DxgiDesktopDuplicator struct {
	framesElapsed int

	mu           sync.Mutex
	displayIndex int
	device       *d3d11.ID3D11Device
	deviceCtx    *d3d11.ID3D11DeviceContext
	ddup         *outputduplication.OutputDuplicator
	screenBounds image.Rectangle
}

func New(displayIndex int) (ss *DxgiDesktopDuplicator, err error) {
	numDisplays := screenshot.NumActiveDisplays()
	if numDisplays <= 0 {
		log.Fatal().
			Msg("screenshot.NumActiveDisplays() <= 0")
	}
	log.Debug().
		Int("numDisplays", numDisplays).
		Msg("active displays")
	maxIndex := numDisplays - 1
	if displayIndex > maxIndex {
		return nil, fmt.Errorf("display index [%d] out of bounds: %d", displayIndex, numDisplays)
	}

	ss = new(DxgiDesktopDuplicator{displayIndex: displayIndex})
	return ss, ss.init()
}

func (ss *DxgiDesktopDuplicator) init() (err error) {
	ss.Close()

	if win.IsValidDpiAwarenessContext(win.DpiAwarenessContextPerMonitorAwareV2) {
		_, err := win.SetThreadDpiAwarenessContext(win.DpiAwarenessContextPerMonitorAwareV2)
		if err != nil {
			log.Warn().
				Err(err).
				Msg("could not set thread DPI awareness to PerMonitorAwareV2")
		} else {
			log.Debug().
				Msg("enabled PerMonitorAwareV2 DPI awareness")
		}
	}

	ss.device, ss.deviceCtx, err = d3d11.NewD3D11Device()
	if err != nil {
		return fmt.Errorf("could not create D3D11 Device: %w", err)
	}

	ss.ddup, err = outputduplication.NewIDXGIOutputDuplication(ss.device, ss.deviceCtx, uint(ss.displayIndex))
	if err != nil {
		return fmt.Errorf("err NewIDXGIOutputDuplication: %w", err)
	}

	ss.screenBounds, err = ss.ddup.GetBounds()
	if err != nil {
		return fmt.Errorf("unable to obtain output bounds: %w", err)
	}

	return nil
}

func (ss *DxgiDesktopDuplicator) Close() (err error) {
	var ret1, ret2 int32
	if ss.ddup != nil {
		ss.ddup.Release()
	}
	if ss.deviceCtx != nil {
		ret1 = ss.deviceCtx.Release()
	}
	if ss.device != nil {
		ret2 = ss.device.Release()
	}
	if ret1 != 0 {
		return fmt.Errorf("ret1 (%d) != 0", ret1)
	}
	if ret2 != 0 {
		return fmt.Errorf("ret2 (%d) != 0", ret2)
	}
	return nil
}

func (ss *DxgiDesktopDuplicator) Bounds() image.Rectangle {
	return ss.screenBounds
}

func (ss *DxgiDesktopDuplicator) FramesElapsed() int {
	return ss.framesElapsed
}

func (ss *DxgiDesktopDuplicator) ResetFramesElapsed() {
	ss.framesElapsed = 0
}

func (ss *DxgiDesktopDuplicator) GetImage(img *image.RGBA) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.getImageTimeout(img, 10)
}

func (ss *DxgiDesktopDuplicator) ProvideMat(dst *gocv.Mat) bool {
	return false
}

func (ss *DxgiDesktopDuplicator) GetImageTimeout(img *image.RGBA, timeoutMs uint) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.getImageTimeout(img, timeoutMs)
}

func (ss *DxgiDesktopDuplicator) getImageTimeout(img *image.RGBA, timeoutMs uint) error {
	b := img.Bounds()
	if b.Dx() != ss.screenBounds.Dx() || b.Dy() != ss.screenBounds.Dy() {
		return ErrSizeChanged
	}

	err := ss.ddup.GetImage(img, timeoutMs)
	if err == nil {
		ss.framesElapsed++
	} else {
		if err == outputduplication.ErrNoImageYet {
			return err
		}
		log.Debug().
			Err(err).
			Msg("renewing duplicator")
		err = ss.init()
		if err != nil {
			return err
		}
		return ss.getImageTimeout(img, timeoutMs)
	}
	return err
}
