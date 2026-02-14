package capture

import (
	"fmt"
	"image"
	"sync"

	"github.com/Miuzarte/SimpleLog"
	"github.com/kbinani/screenshot"
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
	numDisplays := screenshot.NumActiveDisplays()
	if numDisplays <= 0 {
		log.Fatal("screenshot.NumActiveDisplays() <= 0")
	}
	log.Debugf("numDisplays: %d", numDisplays)
	maxIndex := numDisplays - 1
	if displayIndex > maxIndex {
		return nil, fmt.Errorf("display index [%d] out of bounds: %d", displayIndex, numDisplays)
	}

	/*
		// output dup has a reverse order of index (?not tested with more than 2 monitors)
		displayIndex = maxIndex - displayIndex
		log.Debugf("actual displayIndex: %d", displayIndex)
	*/

	ss = new(Capturer{displayIndex: displayIndex})
	return ss, ss.init()
}

func (ss *Capturer) init() (err error) {
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

func (ss *Capturer) Close() (err error) {
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
		if err == outputduplication.ErrNoImageYet {
			return err
		}
		// if errors.Is(err, d3d.HRESULT(d3d.DXGI_ERROR_ACCESS_LOST)) {
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
		err = ss.init()
		if err != nil {
			return err
		} else {
			return ss.getImage(img)
		}
		// }
	}
	return err // [outputduplication.ErrNoImageYet]
}
