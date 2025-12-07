package capture

import (
	"errors"
	"fmt"
	"image"
	"sync"

	"github.com/Miuzarte/SimpleLog"
	"github.com/kbinani/screenshot"
	"github.com/kirides/go-d3d"
	"github.com/kirides/go-d3d/d3d11"
	"github.com/kirides/go-d3d/outputduplication"
	"github.com/kirides/go-d3d/win"
)

var log = SimpleLog.New("[Capture]", true, false)

type Capturer struct {
	FramesElapsed int

	displayIndex int
	device       *d3d11.ID3D11Device
	deviceCtx    *d3d11.ID3D11DeviceContext
	ddup         *outputduplication.OutputDuplicator
	screenBounds image.Rectangle
	mu           sync.Mutex
}

func New(displayIndex int) (ss *Capturer, err error) {
	max := screenshot.NumActiveDisplays()
	if displayIndex >= max {
		return nil, fmt.Errorf("device index [%d] out of bounds: %d", displayIndex, max)
	}

	ss = &Capturer{displayIndex: displayIndex}
	return ss, ss.new()
}

func (ss *Capturer) new() (err error) {
	ss.Close()

	// Make thread PerMonitorV2 Dpi aware if supported on OS
	// allows to let windows handle BGRA -> RGBA conversion and possibly more things
	if win.IsValidDpiAwarenessContext(win.DpiAwarenessContextPerMonitorAwareV2) {
		_, err := win.SetThreadDpiAwarenessContext(win.DpiAwarenessContextPerMonitorAwareV2)
		if err != nil {
			log.Warnf("could not set thread DPI awareness to PerMonitorAwareV2: %v", err)
		} else {
			log.Debugf("enabled PerMonitorAwareV2 DPI awareness")
		}
	}

	// Setup D3D11 stuff
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

func (ss *Capturer) Close() {
	if ss.ddup != nil {
		ss.ddup.Release()
	}
	if ss.deviceCtx != nil {
		ss.deviceCtx.Release()
	}
	if ss.device != nil {
		ss.device.Release()
	}
}

func (ss *Capturer) Bounds() image.Rectangle {
	return ss.screenBounds
}

// do this outside
//
//	runtime.LockOSThread()
//	defer runtime.UnlockOSThread()
func (ss *Capturer) GetImage(img *image.RGBA) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.getImage(img)
}

func (ss *Capturer) getImage(img *image.RGBA) error {
	err := ss.ddup.GetImage(img, 0)
	if err == nil {
		ss.FramesElapsed++
	} else {
		if errors.Is(err, d3d.HRESULT(d3d.DXGI_ERROR_ACCESS_LOST)) {
			/*
				DXGI_ERROR_ACCESS_LOST if the desktop duplication interface is invalid.
				The desktop duplication interface typically becomes invalid
				when a different type of image is displayed on the desktop.
				Examples of this situation are:
					Desktop switch
					Mode change
					Switch from DWM on, DWM off, or other full-screen application
				In this situation,
				the application must release the IDXGIOutputDuplication interface
				and create a new IDXGIOutputDuplication for the new content.
			*/
			log.Debugf("renewing shooter due to: %v", err)
			err = ss.new()
			if err != nil {
				return err
			} else {
				return ss.getImage(img)
			}
		}
	}
	return err // [outputduplication.ErrNoImageYet]
}
