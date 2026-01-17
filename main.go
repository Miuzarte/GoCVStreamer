package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"gioui.org/app"
	"gioui.org/op"

	"github.com/Miuzarte/GoCVStreamer/capture"
	cwg "github.com/Miuzarte/GoCVStreamer/contextWaitGroup"
	"github.com/fsnotify/fsnotify"

	"github.com/Miuzarte/SimpleLog"
	"github.com/kbinani/screenshot"
	"github.com/kirides/go-d3d/outputduplication"
	"github.com/shirou/gopsutil/v4/process"
	"gocv.io/x/gocv"
	"golang.org/x/sys/windows"
)

const (
	DRAW_NEGATIVE_RESULT        = false
	MATCHING_MISJUDGEMENT_ALERT = false
)

var debugging = DEBUGGING

const (
	SAMPLE_RATE      = 5
	SAMPLE_FREQUENCY = time.Second / SAMPLE_RATE
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
	capturer    *capture.Capturer
	screenImage *image.RGBA
	drawEnabled = true
	roiRectSize = image.Point{8 * 11, 8 * 13}
	roiRectPos  = image.Point{2000, 1200}
	// Xmid: 2045
	defaultRoiRect = image.Rectangle{roiRectPos, roiRectPos.Add(roiRectSize)}
	roiRect        = defaultRoiRect
)

var (
	weaponsMu      sync.RWMutex
	weapons        Weapons
	weaponIndex    int
	weaponsMatched int
	weaponFound    bool
)

const WEAPON_INDEX_NONE = -1

// [TODO]? dynamic threshold
const MATCH_THRESHOLD = 0.9

var (
	lastGCStats         debug.GCStats
	captureCost         time.Duration
	weaponsMatchingCost time.Duration
	highLatencyCount    int
	lastHighLatencyTime time.Time
)

var (
	luaFile             *os.File
	luaFileContentIndex = WEAPON_INDEX_NONE
	luaFileContent      []byte
	luaToNoneDebounce   bool
	luaLastSwitchToNone time.Time
	weaponIndexSignal   = make(chan int, 1)
)

var _ = debuggingWaitForInput()

func debuggingWaitForInput() (_ struct{}) {
	if !debugging {
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
	log.Infof("active displays: %d", numDisplays)
	displayBoundaries := make([]image.Rectangle, numDisplays)
	for i := range numDisplays {
		displayBoundaries[i] = screenshot.GetDisplayBounds(i)
	}

	displayIndex := 0
	if numDisplays > 1 {
		log.Info("multi displays detected")
		for i := range numDisplays {
			size := displayBoundaries[i].Size()
			fmt.Fprintf(log.Out, "[%d] %dx%d (X:%d, Y:%d)\n", i, size.X, size.Y, displayBoundaries[i].Min.X, displayBoundaries[i].Min.Y)
		}

		reader := bufio.NewReader(os.Stdin)
		for {
			fmt.Fprintf(log.Out, "input index in range [0,%d]: ", numDisplays-1)
			input, err := reader.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				log.Panicf("failed to read os.Stdin: %v", err)
			}

			input = strings.TrimSpace(input)
			if input != "" {
				index, err := strconv.Atoi(input)
				if err != nil || index < 0 || index >= numDisplays {
					log.Warnf("invalid input %q", input)
					continue
				}
				displayIndex = index
			} else {
				// use the one with max resolution
				maxRes := 0
				for i := range numDisplays {
					size := displayBoundaries[i].Size()
					res := size.X * size.Y
					if res > maxRes {
						maxRes = res
						displayIndex = i
					}
				}
				log.Infof("auto selected display %d", displayIndex)
			}

			break
		}
	}

	displayBounds := displayBoundaries[displayIndex]
	log.Infof("using display [%d] (%dx%d)", displayIndex, displayBounds.Dx(), displayBounds.Dy())

	capturer, err = capture.New(displayIndex)
	panicIf(err)
	if !capturer.Bounds().Eq(displayBounds) {
		log.Warnf("capturer.Bounds() (%v) != displayBounds (%v)", capturer.Bounds(), displayBounds)
	}
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

		var err error
		err = capturer.Close()
		if err != nil {
			log.Errorf("failed to close capturer: %v")
		}
		err = weapons.Close()
		if err != nil {
			log.Errorf("failed to close weapons: %v")
		}
		err = luaFile.Close()
		if err != nil {
			log.Errorf("failed to close luaFile: %v")
		}
	}()

	cwg := cwg.New(context.Background())
	cwg.WithSignal(syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
	defer cwg.Cancel()

	cwg.Go(func(ctx context.Context) {
		cpuMeasureLoop(ctx)
	})
	cwg.Go(func(ctx context.Context) {
		luaSwitchingLoop(ctx)
	})
	cwg.Go(func(ctx context.Context) {
		defer cwg.Cancel()
		windowLoop(ctx)
	})
	cwg.Go(func(ctx context.Context) {
		<-ctx.Done()
		// using ctrl c to exit in console
		// telling the window on background to response
		window.Invalidate()
	})
	cwg.Go(func(ctx context.Context) {
		tmplWatchLoop(ctx)
	})
	cwg.Go(func(ctx context.Context) {
		tmplMatchLoop(ctx)
	})

	cwg.Wait()
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

var forceUpdate = false

func luaSwitchingLoop(ctx context.Context) {
	// the actual duration is exactly a second,
	// more for template matching loop delay
	const debounceInterval = time.Millisecond * 1500

	writeLua := func(newIndex int) {
		weaponsMu.RLock()
		defer weaponsMu.RUnlock()

		var from *Weapon
		fromName := "N/A"
		var fromVal float32
		if luaFileContentIndex >= 0 {
			from = weapons[luaFileContentIndex]
			fromName = from.String()
			fromVal = from.Template.MaxVal
		}

		var to *Weapon
		toName := "N/A"
		var toVal float32
		if newIndex >= 0 {
			to = weapons[newIndex]
			toName = to.String()
			toVal = to.Template.MaxVal
		}

		if forceUpdate {
			forceUpdate = false
		} else if newIndex == luaFileContentIndex {
			return
		} else {
			if MATCHING_MISJUDGEMENT_ALERT &&
				luaFileContentIndex >= 0 && newIndex >= 0 {
				os.Stderr.Write([]byte{'\a'})
			}
			log.Debugf(
				"switching from [%d]%s(%05.2f%%) to [%d]%s(%05.2f%%)",
				luaFileContentIndex, fromName, fromVal*100,
				newIndex, toName, toVal*100,
			)
		}

		// no debounce when debugging
		if !debugging && luaFileContentIndex >= 0 && newIndex == WEAPON_INDEX_NONE {
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

		luaFileContentIndex = newIndex
		luaFileContent = to.Lua(debugging)

		err := luaFile.Truncate(0)
		panicIf(err)
		_, err = luaFile.Seek(0, io.SeekStart)
		panicIf(err)
		_, err = luaFile.Write(luaFileContent)
		panicIf(err)
		err = luaFile.Sync()
		panicIf(err)
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

func windowLoop(ctx context.Context) {
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

func modWeapon(mainOrAlt bool, newSpeed string) {
	if luaFileContentIndex == WEAPON_INDEX_NONE {
		log.Warn("weapon unselected")
		return
	}

	switch newSpeed {
	case "-":
		mainOrAlt = true // only for alt speed
		newSpeed = SPEED_SIGN_AUTO
	case "--":
		mainOrAlt = true
		newSpeed = SPEED_SIGN_COPY
	}

	orig := weapons[luaFileContentIndex]
	dir := filepath.Dir(orig.Path)
	origName := filepath.Base(orig.Path)
	ext := filepath.Ext(origName)

	var speedMain, speedAlt string
	if !mainOrAlt {
		speedMain = newSpeed
		if orig.SpeedAlternativeFrac != 0 {
			speedAlt = fmt.Sprintf("%d.%d", orig.SpeedAlternativeInt, orig.SpeedAlternativeFrac)
		} else {
			speedAlt = fmt.Sprintf("%d", orig.SpeedAlternativeInt)
		}
	} else {
		if orig.SpeedMainFrac != 0 {
			speedMain = fmt.Sprintf("%d.%d", orig.SpeedMainInt, orig.SpeedMainFrac)
		} else {
			speedMain = fmt.Sprintf("%d", orig.SpeedMainInt)
		}
		speedAlt = newSpeed
	}

	newName := fmt.Sprintf("{%s_%s_%s_%s} %s%s",
		orig.Mode.string(true), orig.Type.string(true),
		speedMain, speedAlt,
		orig.Name, ext,
	)
	newPath := filepath.Join(dir, newName)
	err := os.Rename(orig.Path, newPath)
	if err != nil {
		log.Errorf("[main] failed to rename file from %q to %q", orig.Path, newPath)
	} else {
		log.Infof("[main] renamed file from %q to %q", orig.Path, newPath)
	}

	forceUpdate = true
}

func tmplWatchLoop(ctx context.Context) {
	type myFsEvent struct {
		Name        string
		Op          fsnotify.Op
		renamedFrom string
	}
	if unsafe.Sizeof(myFsEvent{}) != unsafe.Sizeof(fsnotify.Event{}) ||
		reflect.TypeOf(myFsEvent{}).NumField() != reflect.TypeOf(fsnotify.Event{}).NumField() {
		log.Panic("[FIXME] definition of fsnotify.Event has been changed")
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

	muAddWeapon := func(name string) error {
		weaponsMu.Lock()
		defer weaponsMu.Unlock()

		time.Sleep(time.Millisecond * 100) // simply wait for the end of writing
		err := weapons.Append(name)
		if err != nil {
			return fmt.Errorf("failed to add new weapon %q: %w", name, err)
		}
		return nil
	}

	muModWeapon := func(from, to string) error {
		weaponsMu.Lock()
		defer weaponsMu.Unlock()

		if strings.HasPrefix(to, TEMPLATES_PREFIX_IGNORE) {
			return nil
		}

		origI := -1
		origName, _, err := parseFileName(from)
		if err == nil {
			origI = weapons.IndexByName(origName)
			// } else {
			// 	ignore
		}

		if origI < 0 {
			// load the new one
			err := weapons.Append(to)
			if err != nil {
				return fmt.Errorf("failed to add new weapon %q: %w", to, err)
			}

		} else {
			// modify

			if strings.HasPrefix(filepath.Base(to), TEMPLATES_PREFIX_IGNORE) {
				// delete
				err := weapons.Delete(origI)
				if err != nil {
					return fmt.Errorf("failed to delete %q: %w", origName, err)
				}
				return nil
			}

			return weapons[origI].DecodeFrom(to)
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
					err = muAddWeapon(event.Name)
				} else {
					// is fsRename
					err = muModWeapon(renameFrom, event.Name)
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

	ticker := time.NewTicker(SAMPLE_FREQUENCY)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case _, ok := <-ticker.C:
			if !ok {
				return
			}

			debug.ReadGCStats(&lastGCStats)

			// screenshot
			tStart := time.Now()
			err := doScreenshot(screenImage, &capture)
			captureCost = time.Since(tStart)
			if err == outputduplication.ErrNoImageYet {
				continue
			}
			panicIf(err)

			if !roiRect.In(capturer.Bounds()) {
				log.Errorf("roiRect %v is not fully contained in screen bounds %v", roiRect, capturer.Bounds())
				continue
			}
			// template match
			captureRoi := capture.Region(roiRect)
			tStart = time.Now()
			weaponIndex, weaponsMatched, weaponFound = doMatchWeapon(captureRoi)
			weaponsMatchingCost = time.Since(tStart)
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

var imageToMatWarnOnce sync.Once

func imageToMat(img image.Image, dst *gocv.Mat) (err error) {
	var src gocv.Mat

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
		src, err = gocv.NewMatFromBytes(y, x, gocv.MatTypeCV8UC4, m.Pix)
		if err != nil {
			return err
		}
		defer src.Close()

	default:
		imageToMatWarnOnce.Do(func() {
			log.Warn("unexpected image color model, conversion performance may be affected")
		})
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

	return gocv.CvtColor(src, dst, gocv.ColorRGBAToBGR)
}

func doScreenshot(dstImage *image.RGBA, dstMat *gocv.Mat) error {
	err := capturer.GetImage(dstImage)
	if err != nil {
		return err
	}
	err = imageToMat(dstImage, dstMat)
	if err != nil {
		return err
	}
	return nil
}

var lastSuccessfulTempl int

func doMatchWeapon(image gocv.Mat) (templateIndex, templateMatched int, found bool) {
	weaponsMu.RLock()
	defer weaponsMu.RUnlock()

	const method = gocv.TmCcoeffNormed

	for j := range weapons {
		i := j + lastSuccessfulTempl // 从上次成功的模板开始往下匹配
		i %= len(weapons)
		templateIndex = i

		tmpl := weapons[i]
		panicIf(tmpl.Template.Match(image, method))

		templateMatched++

		if tmpl.Template.MaxVal >= MATCH_THRESHOLD {
			lastSuccessfulTempl = i
			found = true
			break // 跳过剩余匹配
		}
	}

	return
}
