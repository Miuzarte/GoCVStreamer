package main

import (
	"bufio"
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"math/bits"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"gioui.org/app"
	"gioui.org/io/key"
	"gioui.org/op"

	"github.com/Miuzarte/GoCVStreamer/capture"

	"github.com/Miuzarte/SimpleLog"
	"github.com/kbinani/screenshot"
	"github.com/kirides/go-d3d/outputduplication"
	"github.com/shirou/gopsutil/v4/process"
	"gocv.io/x/gocv"
	"golang.org/x/sys/windows"
)

const (
	DEBUG                       = false
	DRAW_NEGATIVE_RESULT        = false
	MATCHING_MISJUDGEMENT_ALERT = false
)

const (
	TEMPLATES_DIRECTORY = "templates"
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
	origRoiRect  = image.Rect(1986+8, 1197+8, 2114-16, 1306-8)
	roiRect      = origRoiRect
)

var (
	weapons       Weapons
	weaponIndex   int
	weaponMatched = 1
	weaponFound   bool
)

const WEAPON_INDEX_NONE = -1

// [TODO]? dynamic threshold
const MATCH_THRESHOLD = 0.9

var (
	captureCost           time.Duration
	templatesMatchingCost time.Duration
)

var (
	luaFile           *os.File
	luaFileIndexTodo  = WEAPON_INDEX_NONE // intermediate for debounce
	luaFileIndex      = WEAPON_INDEX_NONE
	lastSwitchToNone  time.Time
	weaponIndexSignal = make(chan int, 1)
)

var _ = debugWaitForInput()

func debugWaitForInput() (_ struct{}) {
	if !DEBUG {
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
	window.Option(
		app.Title(windowTitle),
		app.MinSize(1280, 720),
		app.Size(1280, 720),
	)

	processSelf, err = process.NewProcess(int32(processId))
	panicIf(err)

	numDisplays := screenshot.NumActiveDisplays()
	log.Infof("num displays: %d", numDisplays)
	var maxBounds image.Rectangle
	var maxRes int
	for i := range numDisplays {
		bounds := screenshot.GetDisplayBounds(i)
		size := bounds.Size()
		res := size.X * size.Y
		if res > maxRes {
			maxBounds = bounds
			maxRes = res
			displayIndex = i
		}
	}
	log.Infof("using display index: %d(%dx%d)", displayIndex, maxBounds.Dx(), maxBounds.Dy())

	capturer, err = capture.New(displayIndex)
	panicIf(err)
	screenImage = image.NewRGBA(capturer.Bounds())

	err = windows.SetPriorityClass(windows.CurrentProcess(), windows.HIGH_PRIORITY_CLASS)
	if err != nil {
		log.Warnf("failed to set process priority: %v", err)
	}

	// load template
	// [TODO] template files runtime watch using [fsnotify.NewWatcher]
	panicIf(weapons.ReadFrom(TEMPLATES_DIRECTORY, 1, ".png", "__"))
	log.Infof("num templates loaded: %d", len(weapons))

	luaFile, err = os.OpenFile("speed.lua", os.O_WRONLY|os.O_CREATE, 0o664)
	panicIf(err)
}

func main() {
	defer func() {
		capturer.Close()
		panicIf(weapons.Close())
		panicIf(luaFile.Close())
	}()

	wg := sync.WaitGroup{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, _ = signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)

	wg.Go(func() {
		cpuMeasureLoop(ctx)
	})
	wg.Go(func() {
		luaSwitchingLoop(ctx)
	})
	wg.Go(func() {
		windowLoop(ctx, cancel)
	})
	wg.Go(func() {
		<-ctx.Done()
		// using ctrl c to exit in console
		// telling the window on background to response
		window.Invalidate()
	})
	wg.Go(func() {
		tmplMatchLoop(ctx)
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

func luaSwitchingLoop(ctx context.Context) {
	// the actual duration is exactly a second,
	// more for template matching loop delay
	const debounceInterval = time.Millisecond * 1500

	for {
		select {
		case <-ctx.Done():
			return

		case newIndex := <-weaponIndexSignal:
			if newIndex == luaFileIndex {
				continue
			}

			if MATCHING_MISJUDGEMENT_ALERT &&
				luaFileIndexTodo >= 0 && newIndex >= 0 {
				os.Stderr.Write([]byte{'\a'})
			}

			if luaFileIndex >= 0 && newIndex == WEAPON_INDEX_NONE {
				// from notnone to none
				if luaFileIndexTodo != WEAPON_INDEX_NONE {
					// going to none, enter debounce
					luaFileIndexTodo = WEAPON_INDEX_NONE
					lastSwitchToNone = time.Now()
					continue
				} else {
					// debounce skipping
					timeToNone := lastSwitchToNone.Add(debounceInterval)
					if time.Now().Before(timeToNone) {
						log.Debug("switching skipped due to debounce")
						continue
					}
				}
			} else {
				luaFileIndexTodo = newIndex
			}

			if luaFileIndex >= 0 {
				from := weapons[luaFileIndex]
				toName := "N/A"
				if luaFileIndexTodo >= 0 {
					toName = weapons[luaFileIndexTodo].Name
				}
				log.Debugf(
					"switch from %d(%s) to %d(%s), last maxVal: %.2f%%",
					luaFileIndex, from.Name,
					luaFileIndexTodo, toName,
					from.Template.MaxVal*100,
				)
			}
			luaFileIndex = luaFileIndexTodo

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
			switch {
			case luaFileIndexTodo == WEAPON_INDEX_NONE:
				content = "FAA=0\nFA1=0\nSAA=0\nSA1=0"

			case luaFileIndexTodo < 0:
				log.Panicf("unreachable: %d", luaFileIndexTodo)

			default:
				weapon := weapons[luaFileIndexTodo]
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

			if screenImage != nil {
				layoutDisplay(gtx, screenImage)
			}
			if drawEnabled {
				layoutGocvInfo(gtx)
			}

			e.Frame(gtx.Ops)

		case app.ConfigEvent:
		// case app.wakeupEvent:
		default:
			log.Tracef("event[%T]: %v", e, e)
		}
	}
}

func shortcutListWeapons(key.Name, key.Modifiers) {
	for i, tmpl := range weapons {
		fmt.Printf("[%d] %s %.2f%%\n", i, tmpl.Name, tmpl.Template.MaxVal*100)
	}
}

func shortcutPrintProcess(key.Name, key.Modifiers) {
	windowHandel = windows.GetForegroundWindow()
	log.Infof("parent process id: %d", parentProcessId)
	log.Infof("process id: %d", processId)
	log.Infof("window handel: %#X", windowHandel)
}

func shortcutResetFreamsElapsed(key.Name, key.Modifiers) {
	capturer.FramesElapsed = 0
	log.Info("capturer.FramesElapsed reset")
}

var drawEnabled = true

func shortcutToggleDraw(key.Name, key.Modifiers) {
	drawEnabled = !drawEnabled
}

var showPosTill time.Time

func shortcutMoveRoiRect(name key.Name, mod key.Modifiers) {
	offset := 1
	for range bits.OnesCount32(uint32(mod)) {
		offset *= 4
	}

	var newRect image.Rectangle

	switch name {
	case "R", "r":
		newRect = origRoiRect
	case key.NameUpArrow:
		newRect = roiRect.Sub(image.Pt(0, offset))
	case key.NameDownArrow:
		newRect = roiRect.Add(image.Pt(0, offset))
	case key.NameLeftArrow:
		newRect = roiRect.Sub(image.Pt(offset, 0))
	case key.NameRightArrow:
		newRect = roiRect.Add(image.Pt(offset, 0))
	}

	boundaryCheck(capturer.Bounds(), &newRect)
	roiRect = newRect
	showPosTill = time.Now().Add(time.Second * 3)
	log.Debugf("roiRect: %v", roiRect)
}

func boundaryCheckPos(constraints image.Rectangle, pos *image.Point) {
	pos.X = max(pos.X, constraints.Min.X)
	pos.Y = max(pos.Y, constraints.Min.Y)
	pos.X = min(pos.X, constraints.Max.X)
	pos.Y = min(pos.Y, constraints.Max.Y)
}

func boundaryCheck(constraints image.Rectangle, rect *image.Rectangle) {
	size := rect.Size()
	boundaryCheckPos(constraints, &rect.Max)
	rect.Min = rect.Max.Sub(size)
	boundaryCheckPos(constraints, &rect.Min)
	rect.Max = rect.Min.Add(size)
}

func shortcutSetWda(_ key.Name, mod key.Modifiers) {
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
		var toWda uint32
		if !mod.Contain(key.ModShift) {
			toWda = WDA_EXCLUDEFROMCAPTURE
			log.Info("wda set to WDA_EXCLUDEFROMCAPTURE")
		} else {
			toWda = WDA_MONITOR
			log.Info("wda set to WDA_MONITOR")
		}
		err = SetWindowDisplayAffinity(windowHandel, toWda)
	case WDA_EXCLUDEFROMCAPTURE:
		log.Info("wda set to WDA_NONE")
		err = SetWindowDisplayAffinity(windowHandel, WDA_NONE)
	}

	if err != nil {
		log.Errorf("failed to SetWindowDisplayAffinity: %v", err)
	}
}

func tmplMatchLoop(ctx context.Context) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	capture := gocv.NewMat()
	defer capture.Close()

	ticker := time.NewTicker(time.Millisecond * 200)
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

			// template match
			captureRoi := capture.Region(roiRect)
			tStart = time.Now()
			weaponIndex, weaponMatched, weaponFound = doMatchWeapon(captureRoi)
			templatesMatchingCost = time.Since(tStart)
			captureRoi.Close()

			// output
			if weaponFound {
				weaponIndexSignal <- weaponIndex
			} else {
				weaponIndexSignal <- WEAPON_INDEX_NONE
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

		if tmpl.Template.MaxVal >= MATCH_THRESHOLD {
			lastSuccessfulTempl = i
			found = true
			break // 跳过剩余匹配
		}
	}

	return
}
