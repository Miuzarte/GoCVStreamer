package main

import (
	"bufio"
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"gioui.org/app"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/Miuzarte/GoCVStreamer/capture"

	"github.com/Miuzarte/SimpleLog"
	"github.com/kbinani/screenshot"
	"github.com/kirides/go-d3d/outputduplication"
	"github.com/shirou/gopsutil/v4/process"
	"gocv.io/x/gocv"
	"golang.org/x/sys/windows"
)

const (
	debug   = false
	drawNeg = false
)

const (
	templatesDir = "templates"
)

var log = SimpleLog.New("[Streamer]", true, false).SetLevel(SimpleLog.DebugLevel)

var (
	parentProcessId = os.Getppid()
	processId       = os.Getpid()
	windowHandel    windows.HWND
	windowTitle     = strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")

	processSelf *process.Process
)

var (
	displayIndex = 0 // [TODO] 自动取分辨率最高的屏幕
	capturer     *capture.Capturer
	screenImage  *image.RGBA
	roiRect      = image.Rect(1986+8, 1197+8, 2114-16, 1306-8)
	draggableRoi = DraggableRoi{rect: roiRect}
)

var (
	weapons       Weapons
	weaponIndex   int
	weaponMatched = 1
	weaponFound   bool
)

// [TODO]? dynamic threshold
const matchThreshold = 0.9

var (
	captureCost           time.Duration
	templatesMatchingCost time.Duration
	drawCost              time.Duration
)

var (
	luaFile      *os.File
	luaFileIndex = -1
	outputSignal = make(chan int, 1)
)

var _ = debugWaitForInput()

func debugWaitForInput() (_ struct{}) {
	if !debug {
		return
	}
	log.Debugf("pid: %d, ppid: %d", processId, parentProcessId)
	fmt.Print("waiting for any input...")
	_, err := bufio.NewReader(os.Stdin).ReadBytes('\n')
	if err == io.EOF {
		err = nil
	}
	panicIf(err)
	return
}

func init() {
	var err error

	if windowTitle == "" {
		log.Panic("failed to initialize window name")
	}

	processSelf, err = process.NewProcess(int32(processId))
	panicIf(err)

	numDisplays := screenshot.NumActiveDisplays()
	log.Infof("num displays: %d", numDisplays)
	switch {
	case numDisplays == 0:
		log.Panic("display not found")
	case numDisplays > 1:
		log.Info("[TODO] multi displays select")
	}

	capturer, err = capture.New(displayIndex)
	panicIf(err)
	log.Infof("display bounds: %dx%d", capturer.Bounds().Dx(), capturer.Bounds().Dy())
	screenImage = image.NewRGBA(capturer.Bounds())

	err = windows.SetPriorityClass(windows.CurrentProcess(), windows.HIGH_PRIORITY_CLASS)
	if err != nil {
		log.Warnf("failed to set process priority: %v", err)
	}

	// load template
	panicIf(weapons.ReadFrom(templatesDir, 1, ".png", "__"))
	log.Infof("num templates loaded: %d", len(weapons))

	luaFile, err = os.OpenFile("speed.lua", os.O_WRONLY|os.O_CREATE, 0o664)
	panicIf(err)
}

func main() {
	defer func() {
		panicIf(weapons.Close())
	}()

	wg := sync.WaitGroup{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, _ = signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)

	window.Option(
		app.Title(windowTitle),
		app.MinSize(1280, 720),
		app.Size(1280, 720),
	)

	wg.Go(func() {
		cpuMeasureLoop(ctx)
	})
	wg.Go(func() {
		outputLuaLoop(ctx)
	})
	wg.Go(func() {
		windowLoop(ctx, cancel)
	})
	wg.Go(func() {
		captureMatchLoop(ctx)
	})

	wg.Wait()
}

func cpuMeasureLoop(ctx context.Context) {
	const interval = time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
			cpu, _ = processSelf.PercentWithContext(ctx, interval)
		}
	}
}

func outputLuaLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return

		case weaponIndex := <-outputSignal:
			if weaponIndex == luaFileIndex {
				continue
			}

			if luaFileIndex >= 0 && weaponIndex >= 0 {
				os.Stderr.Write([]byte{'\a'})
			}
			if luaFileIndex >= 0 {
				from := weapons[luaFileIndex]
				var to *Weapon
				var toName string
				if weaponIndex >= 0 {
					to = weapons[weaponIndex]
					toName = to.Name
				}
				log.Debugf(
					"switch from %d(%s) to %d(%s), last maxVal: %.2f",
					luaFileIndex, from.Name,
					weaponIndex, toName,
					from.Template.MaxVal*100,
				)
			}
			luaFileIndex = weaponIndex
			err := luaFile.Truncate(0)
			if err != nil {
				log.Warnf("luaFile failed to Truncate: %v", err)
				continue
			}
			_, err = luaFile.Seek(0, io.SeekStart)
			if err != nil {
				log.Warnf("luaFile failed to Seek: %v", err)
				continue
			}

			var content string
			if weaponIndex < 0 { // not found, all zero
				content = "FAA=0\nFA1=0\nSAA=0\nSA1=0"
			} else {
				weapon := weapons[weaponIndex]
				switch weapon.Mode {
				case WEAPON_MODE_FULL_AUTO:
					content = "FAA=" + weapon.SpeedAcog +
						"\n" + "FA1=" + weapon.Speed1x +
						"\n" + "SAA=0" + "\n" + "SA1=0"
				case WEAPON_MODE_SEMI_AUTO:
					content = "FAA=0" + "\n" + "FA1=0" +
						"\n" + "SAA=" + weapon.SpeedAcog +
						"\n" + "SA1=" + weapon.Speed1x
				default:
					log.Warnf("unexpected tmpl.Mode: %s", weapon.Mode)
					content = "FAA=0" + "\n" + "FA1=0" +
						"\n" + "SAA=0" + "\n" + "SA1=0"
				}
			}

			_, err = luaFile.WriteString(content)
			if err != nil {
				log.Warnf("luaFile failed to WriteString: %v", err)
				continue
			}
			err = luaFile.Sync()
			if err != nil {
				log.Warnf("luaFile failed to Sync: %v", err)
				continue
			}
		}
	}
}

var (
	roiDraggable widget.Draggable
	roiDragStart image.Rectangle // 拖动开始时ROI的原始位置
	roiMutex     sync.RWMutex    // 保护roiRect的并发访问
)

func windowLoop(ctx context.Context, cancel context.CancelFunc) {
	defer cancel()
	var ops op.Ops
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		switch e := window.Event().(type) {
		case app.DestroyEvent:
			if e.Err != nil {
				log.Errorf("window error: %v", e.Err)
			} else {
				log.Debug("window closed normally")
			}
			return

		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			dScale = gtx.Metric

			err := shortcuts.Match(gtx)
			if err != nil {
				log.Warnf("shortcuts match error: %v", err)
			}

			tStart := time.Now()
			if screenImage != nil {
				layoutDisplay(gtx, screenImage)
			}
			layoutGocvInfo(gtx)
			drawCost = time.Since(tStart)

			e.Frame(gtx.Ops)

		case app.ConfigEvent:
		// case app.wakeupEvent:
		default:
			log.Tracef("event[%T]: %v", e, e)
		}
	}
}

func shortcutListWeapons() {
	for i, tmpl := range weapons {
		fmt.Printf("[%d] %s %.2f%%\n", i, tmpl.Name, tmpl.Template.MaxVal*100)
	}
}

func shortcutPrintProcess() {
	windowHandel = windows.GetForegroundWindow()
	log.Infof("parent process id: %d", parentProcessId)
	log.Infof("process id: %d", processId)
	log.Infof("window handel: %#X", windowHandel)
}

func shortcutResetFreamsElapsed() {
	capturer.FramesElapsed = 0
	log.Info("capturer.FramesElapsed reset")
}

func shortcutResetRoi() {
	draggableRoi.rect = roiRect
	log.Info("roi reset done")
}

func shortcutSetWda() {
	if windowHandel == 0 {
		windowHandel = windows.GetForegroundWindow()
	}

	currWda, err := GetWindowDisplayAffinity(windowHandel)
	if err != nil {
		log.Errorf("failed to GetWindowDisplayAffinity: %v", err)
		return
	}

	switch currWda {
	case WDA_NONE:
		currWda = WDA_EXCLUDEFROMCAPTURE
		log.Info("wda set to WDA_EXCLUDEFROMCAPTURE")
		err = SetWindowDisplayAffinity(windowHandel, WDA_EXCLUDEFROMCAPTURE)
	case WDA_EXCLUDEFROMCAPTURE:
		currWda = WDA_NONE
		log.Info("wda set to WDA_NONE")
		err = SetWindowDisplayAffinity(windowHandel, WDA_NONE)
	}

	if err != nil {
		log.Errorf("failed to SetWindowDisplayAffinity: %v", err)
	}
}

func captureMatchLoop(ctx context.Context) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	capture := gocv.NewMat()
	defer capture.Close()

	ticker := time.NewTicker(time.Millisecond * 128)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			// screenshot
			tStart := time.Now()
			err := doScreenshot(screenImage, &capture)
			captureCost = time.Since(tStart)
			if err == outputduplication.ErrNoImageYet {
				continue
			}
			panicIf(err)

			if draggableRoi.Draggable.Dragging() {
				continue
			}
			currSize := draggableRoi.rect.Size()
			origSize := roiRect.Size()
			if currSize.X < origSize.X || currSize.Y < origSize.Y {
				log.Warnf("draggableRoi.rect < roiRect: %v", currSize)
				continue
			}

			// template match
			captureRoi := capture.Region(draggableRoi.rect)
			tStart = time.Now()
			weaponIndex, weaponMatched, weaponFound = doMatchWeapon(captureRoi)
			templatesMatchingCost = time.Since(tStart)
			captureRoi.Close()

			// output
			if weaponFound {
				outputSignal <- weaponIndex
			} else {
				outputSignal <- -1
			}

			window.Invalidate()
		}
	}
}

func imageToMat(img image.Image, dst *gocv.Mat) error {
	bounds := img.Bounds()
	x := bounds.Dx()
	y := bounds.Dy()
	switch img.ColorModel() {
	case color.RGBAModel:
		m, res := img.(*image.RGBA)
		if true != res {
			return fmt.Errorf("image color format error")
		}
		// speed up the conversion process of RGBA format
		src, err := gocv.NewMatFromBytes(y, x, gocv.MatTypeCV8UC4, m.Pix)
		if err != nil {
			return err
		}
		defer src.Close()

		gocv.CvtColor(src, dst, gocv.ColorRGBAToBGR)

	default:
		data := make([]byte, 0, x*y*3)
		for j := bounds.Min.Y; j < bounds.Max.Y; j++ {
			for i := bounds.Min.X; i < bounds.Max.X; i++ {
				r, g, b, _ := img.At(i, j).RGBA()
				data = append(data, byte(b>>8), byte(g>>8), byte(r>>8))
			}
		}
		src, err := gocv.NewMatFromBytes(y, x, gocv.MatTypeCV8UC3, data)
		if err != nil {
			return err
		}
		defer src.Close()
	}
	return nil
}

func doScreenshot(screenImage *image.RGBA, display *gocv.Mat) error {
	err := capturer.GetImage(screenImage)
	if err != nil {
		return err
	}
	err = imageToMat(screenImage, display)
	if err != nil {
		return err
	}
	return nil
}

var lastSuccessfulTempl int

func doMatchWeapon(captureRoi gocv.Mat) (templateIndex, templateMatched int, found bool) {
	const method = gocv.TmCcoeffNormed

	for j := range weapons {
		i := j + lastSuccessfulTempl // 从上次成功的模板开始往下匹配
		i %= len(weapons)
		templateIndex = i

		tmpl := weapons[i]
		panicIf(tmpl.Template.Match(captureRoi, method))

		templateMatched++

		if tmpl.Template.MaxVal >= matchThreshold {
			lastSuccessfulTempl = i
			found = true
			break // 跳过剩余匹配
		}
	}

	return
}
