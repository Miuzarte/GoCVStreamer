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
	"reflect"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"gioui.org/app"
	"gioui.org/io/key"
	"gioui.org/op"

	"github.com/Miuzarte/GoCVStreamer/capture"
	"github.com/fsnotify/fsnotify"

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
	TEMPLATES_DIRECTORY     = "templates"
	TEMPLATES_SUFFIX        = ".png"
	TEMPLATES_PREFIX_IGNORE = "__"
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
	displayIndex = 0
	capturer     *capture.Capturer
	screenImage  *image.RGBA
	origRoiRect  = image.Rect(1986+8, 1197+8, 2114-16, 1306-8)
	roiRect      = origRoiRect
)

var (
	weaponsMu     sync.RWMutex
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
	luaFile             *os.File
	luaFileIndex        = WEAPON_INDEX_NONE
	luaToNoneDebounce   bool
	luaLastSwitchToNone time.Time
	weaponIndexSignal   = make(chan int, 1)
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

	loadTemplates()

	luaFile, err = os.OpenFile("speed.lua", os.O_WRONLY|os.O_CREATE, 0o664)
	panicIf(err)
}

func loadTemplates() {
	weaponsMu.Lock()
	defer weaponsMu.Unlock()

	tStart := time.Now()
	if len(weapons) != 0 {
		panicIf(weapons.Close())
	}
	panicIf(weapons.ReadFrom(TEMPLATES_DIRECTORY, 1, TEMPLATES_SUFFIX, TEMPLATES_PREFIX_IGNORE))
	log.Infof("%d template(s) loaded cost %s", len(weapons), time.Since(tStart))
}

func main() {
	defer func() {
		weaponsMu.Lock()
		defer weaponsMu.Unlock()

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
		tmplWatchLoop(ctx)
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

	writeLua := func(newIndex int) {
		weaponsMu.RLock()
		defer weaponsMu.RUnlock()

		if newIndex == luaFileIndex {
			return
		}

		var from *Weapon
		var to *Weapon
		fromName := "N/A"
		toName := "N/A"
		var fromVal float32
		var toVal float32
		if luaFileIndex >= 0 {
			from = weapons[luaFileIndex]
			fromName = from.Name
			fromVal = from.Template.MaxVal
		}
		if newIndex >= 0 {
			to = weapons[newIndex]
			toName = to.Name
			toVal = to.Template.MaxVal
		}

		log.Debugf(
			"switching from [%d]%s(%05.2f%%) to [%d]%s(%05.2f%%)",
			luaFileIndex, fromName, fromVal*100,
			newIndex, toName, toVal*100,
		)

		if MATCHING_MISJUDGEMENT_ALERT &&
			luaFileIndex >= 0 && newIndex >= 0 {
			os.Stderr.Write([]byte{'\a'})
		}

		if luaFileIndex >= 0 && newIndex == WEAPON_INDEX_NONE {
			// from notnone to none
			if !luaToNoneDebounce {
				// going to none, enter debounce
				luaToNoneDebounce = true
				luaLastSwitchToNone = time.Now()
				return
			} else {
				// debounce skipping
				timeToNone := luaLastSwitchToNone.Add(debounceInterval)
				if time.Now().Before(timeToNone) {
					log.Debug("switching skipped due to debounce")
					return
				}
				// exit debounce
				luaToNoneDebounce = false
			}
		}

		luaFileIndex = newIndex

		err := luaFile.Truncate(0)
		if err != nil {
			log.Warnf("luaFile failed to Truncate: %v", err)
			return
		}
		_, err = luaFile.Seek(0, io.SeekStart)
		if err != nil {
			log.Warnf("luaFile failed to Seek: %v", err)
			return
		}

		const defaultContent = "FAA=0\nFA1=0\nSAA=0\nSA1=0"
		var content string
		switch {
		case luaFileIndex == WEAPON_INDEX_NONE:
			content = defaultContent

		case luaFileIndex < 0:
			log.Panicf("unreachable: %d", luaFileIndex)

		default:
			switch to.Mode {
			case WEAPON_MODE_FULL_AUTO:
				content = "FAA=" + to.SpeedAcog +
					"\n" + "FA1=" + to.Speed1x +
					"\n" + "SAA=0" + "\n" + "SA1=0"
			case WEAPON_MODE_SEMI_AUTO:
				content = "FAA=0" + "\n" + "FA1=0" +
					"\n" + "SAA=" + to.SpeedAcog +
					"\n" + "SA1=" + to.Speed1x
			default:
				log.Warnf("unexpected tmpl.Mode: %s", to.Mode)
				content = defaultContent
			}
		}

		_, err = luaFile.WriteString(content)
		if err != nil {
			log.Warnf("luaFile failed to WriteString: %v", err)
			return
		}
		err = luaFile.Sync()
		if err != nil {
			log.Warnf("luaFile failed to Sync: %v", err)
			return
		}
	}

	for {
		select {
		case <-ctx.Done():
			return

		case newIndex := <-weaponIndexSignal:
			// using closure for mutex control
			writeLua(newIndex)
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
	weaponsMu.RLock()
	defer weaponsMu.RUnlock()

	for i, tmpl := range weapons {
		fmt.Printf("[%d] {%s_%s_%s}%s %.2f%%\n", i,
			tmpl.Mode.string(true), tmpl.SpeedAcog, tmpl.Speed1x, tmpl.Name,
			tmpl.Template.MaxVal*100)
	}
}

// Deprecated: there is a slight memory leak
func shortcutReloadWeapons(_ key.Name, mod key.Modifiers) {
	if mod.Contain(key.ModCtrl | key.ModShift) {
		loadTemplates()
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

func tmplWatchLoop(ctx context.Context) {
	type myFsEvent struct {
		Name        string
		Op          fsnotify.Op
		renamedFrom string
	}
	if unsafe.Sizeof(myFsEvent{}) != unsafe.Sizeof(fsnotify.Event{}) ||
		reflect.TypeOf(myFsEvent{}).NumField() != reflect.TypeOf(fsnotify.Event{}).NumField() {
		log.Panic("[FIXME] define of fsnotify.Event has been changed")
	}

	watcher, err := fsnotify.NewWatcher()
	panicIf(err)
	defer watcher.Close()
	panicIf(watcher.Add(TEMPLATES_DIRECTORY))

	// wrap mutex with closures
	muDelWeapon := func(event fsnotify.Event) (int, error) {
		weaponsMu.Lock()
		defer weaponsMu.Unlock()

		n, err := weapons.DeleteByPath(event.Name)
		if err != nil {
			return n, fmt.Errorf("failed to delete weapon %q: %w", event.Name, err)
		}
		return n, nil
	}

	muAddWeapon := func(event fsnotify.Event) error {
		weaponsMu.Lock()
		defer weaponsMu.Unlock()

		time.Sleep(time.Millisecond * 100) // simply wait for the end of writing
		err := weapons.Append(event.Name)
		if err != nil {
			return fmt.Errorf("failed to add new weapon %q: %w", event.Name, err)
		}
		return nil
	}

	muModWeapon := func(event fsnotify.Event) error {
		weaponsMu.Lock()
		defer weaponsMu.Unlock()

		renameFrom := (*myFsEvent)(unsafe.Pointer(&event)).renamedFrom

		if strings.HasPrefix(event.Name, TEMPLATES_PREFIX_IGNORE) {
			return nil
		}

		origI := -1
		origName, _, err := parseFileName(renameFrom)
		if err == nil {
			origI = weapons.IndexByName(origName)
			// } else {
			// 	ignore
		}

		if origI < 0 {
			// load the new one
			err := weapons.Append(event.Name)
			if err != nil {
				return fmt.Errorf("failed to add new weapon %q: %w", event.Name, err)
			}
		} else {
			// modify
			name, params, err := parseFileName(event.Name)
			if err != nil {
				return fmt.Errorf("failed to parse file %q to modify: %w", name, err)
			}
			if strings.HasPrefix(filepath.Base(event.Name), TEMPLATES_PREFIX_IGNORE) {
				// delete
				_, err := weapons.DeleteByName(name)
				if err != nil {
					return fmt.Errorf("failed to delete %q: %w", name, err)
				}
				return nil
			}

			if name != origName {
				// wried
				log.Warnf("weapon %q name was changed to %q", origName, name)
			}

			w := weapons[origI]
			switch params[0] {
			case "FA":
				w.Mode = WEAPON_MODE_FULL_AUTO
			case "SA":
				w.Mode = WEAPON_MODE_SEMI_AUTO
			default:
				return fmt.Errorf("invalid file params mode: %s", params[0])
			}
			w.Path = event.Name
			w.Name = name
			w.SpeedAcog = params[1]
			w.Speed1x = params[2]
		}

		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return

		case event, ok := <-watcher.Events:
			if !ok {
				return
			}

			log.Debugf("fs event: %s", event)

			/*
				[14:13:57.771]CREATE        "templates\\config.ini"
				[14:13:57.771]WRITE         "templates\\config.ini"
				[14:13:57.771]WRITE         "templates\\config.ini"

				[14:14:44.191]RENAME        "templates\\config.ini"
				[14:14:44.191]CREATE        "templates\\config__.ini" ← "templates\\config.ini"

				[14:16:47.031]REMOVE        "templates\\config__.ini"
			*/
			switch event.Op {
			// case fsnotify.Rename:
			// handled in next fsCreate signal

			case fsnotify.Create:
				var err error
				renameFrom := (*myFsEvent)(unsafe.Pointer(&event)).renamedFrom

				if renameFrom == "" {
					// is fsCreate
					err = muAddWeapon(event)
				} else {
					// is fsRename
					err = muModWeapon(event)
				}
				if err != nil {
					log.Warn(err)
					continue
				}

				if renameFrom == "" {
					log.Infof("weapon %q added successfully", event.Name)
				} else {
					log.Infof("weapon %q modified successfully", event.Name)
				}

			case fsnotify.Remove:
				deleted, err := muDelWeapon(event)
				if err != nil {
					log.Warn(err)
					continue
				}

				switch deleted {
				case 1:
					log.Infof("weapon %q deleted successfully", event.Name)
				case 0:
					log.Warnf("weapon %q failed to delete", event.Name)
				default:
					log.Warnf("weapon %q triggered multiple weapons deletion: %d", event.Name, deleted)
				}

			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Errorf("fsnotify error: %v", err)

		}
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

		case _, ok := <-ticker.C:
			if !ok {
				return
			}

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
	weaponsMu.RLock()
	defer weaponsMu.RUnlock()

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
