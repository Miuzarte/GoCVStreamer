package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"io"
	"math/bits"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/Miuzarte/GoCVStreamer/capturer"
	cwg "github.com/Miuzarte/GoCVStreamer/contextWaitGroup"
	"github.com/Miuzarte/GoCVStreamer/detector"
	"github.com/Miuzarte/GoCVStreamer/logger"
	"github.com/Miuzarte/GoCVStreamer/matcher"
	"github.com/Miuzarte/GoCVStreamer/mouse"
	"github.com/Miuzarte/GoCVStreamer/recoil"
	"github.com/Miuzarte/GoCVStreamer/ui"
	w "github.com/Miuzarte/GoCVStreamer/weapon"
	ws "github.com/Miuzarte/GoCVStreamer/weapons"
	"github.com/Miuzarte/GoCVStreamer/widgets"
	"github.com/fsnotify/fsnotify"
	"github.com/kbinani/screenshot"
	"github.com/shirou/gopsutil/v4/process"
	"gocv.io/x/gocv"
	"golang.org/x/sys/windows"
)

var debugging = DEBUGGING

var (
	nogui    = flag.Bool("nogui", false, "run without GUI window")
	httpPort = flag.String("port", ":8080", "HTTP metrics server port")
	nohttp   = flag.Bool("nohttp", false, "disable HTTP metrics server")
	noyolo   = flag.Bool("noyolo", false, "disable YOLO person detection")
)

var log = logger.New("Streamer")

const (
	TEMPLATES_DIRECTORY     = "templates"
	TEMPLATES_SUFFIX        = ".png"
	TEMPLATES_PREFIX_IGNORE = "__"
)

const (
	CREATE_MASK                   = false
	MATCHING_MODE gocv.IMReadFlag = gocv.IMReadGrayScale
)

var (
	processSelf     *process.Process
	parentProcessId = os.Getppid()
	processId       = os.Getpid()
	windowHandel    windows.HWND
	windowTitle     = strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")

	weaponsMu sync.RWMutex
	weapons   ws.Weapons
)

var (
	roiRectSize = image.Point{8 * 11, 8 * 13}
	roiRectPos  = image.Point{2000, 1200}
	// Xmid: 2045
	roiRect = image.Rectangle{roiRectPos, roiRectPos.Add(roiRectSize)}
)

var (
	capturerServer *capturer.Server
	matcherEngine  *matcher.Engine
	detectorEngine *detector.Engine
	recoilEngine   *recoil.Engine
	window         *ui.Window

	cpu         float64
	forceUpdate bool
)

var _ = debuggingWaitForInput()

func debuggingWaitForInput() (_ struct{}) {
	if !debugging {
		return
	}
	log.Debug().
		Int("pid", processId).
		Int("ppid", parentProcessId).
		Msg("process ids")
	fmt.Print("waiting for any input...")
	_, err := bufio.NewReader(os.Stdin).ReadBytes('\n')
	if err == io.EOF {
		err = nil
	}
	panicIf(err)
	return
}

func init() {
	flag.Parse()

	var err error

	err = windows.SetPriorityClass(windows.CurrentProcess(), windows.HIGH_PRIORITY_CLASS)
	if err != nil {
		log.Warn().Err(err).Msg("failed to set process priority")
	}

	processSelf, err = process.NewProcess(int32(processId))
	if err != nil {
		log.Panic().Err(err).Msg("failed to get process")
	}

	selectDisplay()

	loadTemplates()
}

func selectDisplay() {
	displayIndex, err := selectDisplayInteractive()
	if err != nil {
		log.Panic().Err(err).Msg("failed to select display")
	}

	duplicator, err := capturer.New(displayIndex)
	if err != nil {
		log.Panic().Err(err).Msg("failed to create capturer")
	}

	bounds := duplicator.Bounds()
	log.Info().
		Int("displayIndex", displayIndex).
		Int("width", bounds.Dx()).
		Int("height", bounds.Dy()).
		Msg("using display")

	roiRect = image.Rectangle{roiRectPos, roiRectPos.Add(roiRectSize)}

	ui.InitTheme()

	capturerServer = capturer.NewServer(duplicator, MATCHING_MODE, func() {
		if window != nil && !*nogui {
			window.App().Invalidate()
		}
	})
}

func selectDisplayInteractive() (int, error) {
	numDisplays := screenshot.NumActiveDisplays()
	log.Info().
		Int("activeDisplays", numDisplays).
		Msg("active displays detected")

	displayBounds := make([]image.Rectangle, numDisplays)
	for i := range numDisplays {
		displayBounds[i] = screenshot.GetDisplayBounds(i)
	}

	if numDisplays <= 1 {
		return 0, nil
	}

	log.Info().
		Msg("multi displays detected")
	for i := range numDisplays {
		size := displayBounds[i].Size()
		fmt.Fprintf(os.Stdout, "[%d] %dx%d (X:%d, Y:%d)\n", i, size.X, size.Y, displayBounds[i].Min.X, displayBounds[i].Min.Y)
	}

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Fprintf(os.Stdout, "input index in range [0,%d]: ", numDisplays-1)
		input, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, fmt.Errorf("failed to read os.Stdin: %w", err)
		}

		input = strings.TrimSpace(input)
		if input != "" {
			index, err := strconv.Atoi(input)
			if err != nil || index < 0 || index >= numDisplays {
				log.Warn().
					Str("input", input).
					Int("min", 0).
					Int("max", numDisplays-1).
					Msg("invalid display index input")
				continue
			}
			return index, nil
		}

		maxRes := 0
		best := 0
		for i := range numDisplays {
			size := displayBounds[i].Size()
			res := size.X * size.Y
			if res > maxRes {
				maxRes = res
				best = i
			}
		}
		log.Info().
			Int("displayIndex", best).
			Msg("auto selected display")
		return best, nil
	}
}

func loadTemplates() {
	weaponsMu.Lock()
	defer weaponsMu.Unlock()

	tStart := time.Now()
	if len(weapons) != 0 {
		panicIf(weapons.Close())
	}
	panicIf(weapons.ReadFrom(TEMPLATES_DIRECTORY, 1, TEMPLATES_SUFFIX, TEMPLATES_PREFIX_IGNORE, CREATE_MASK, MATCHING_MODE))
	log.Info().
		Int("templates", len(weapons)).
		Dur("cost", time.Since(tStart)).
		Msg("templates loaded")
}

func main() {
	defer func() {
		weaponsMu.Lock()
		defer weaponsMu.Unlock()

		if capturerServer != nil {
			capturerServer.Close()
		}
		weapons.Close()
		if detectorEngine != nil {
			detectorEngine.Close()
		}
	}()

	cwg := cwg.New(context.Background())
	cwg.WithSignal(syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
	defer cwg.Cancel()

	if !*nohttp {
		cwg.Go(func(ctx context.Context) {
			startHttpServer(ctx, *httpPort)
		})
	}

	matcherCfg := matcher.Config{
		Fps:              5,
		FpsIdle:          2,
		DropIdleDuration: time.Second * 5,

		Weapons:   weapons,
		WeaponsMu: &weaponsMu,
		RoiRect:   roiRect,
		Debugging: debugging,
	}
	matcherEngine = matcher.New(capturerServer, matcherCfg)

	clicker := mouse.NewClicker(cwg.Ctx)
	recoilCfg := recoil.Config{
		Debugging: debugging,
	}
	recoilEngine = recoil.New(clicker, recoilCfg)

	if !*noyolo {
		detectorEngine = initDetector()
	}
	if detectorEngine == nil {
		log.Warn().
			Msg("detector disabled")
	}

	if !*nogui {
		window = ui.NewWindow(ui.Config{
			Title:   windowTitle,
			MinSize: image.Pt(1280, 720),
			Size:    image.Pt(1280, 720),
		})
		window.SetShortcuts(createShortcuts(window.App()))
		window.Register(&metricsDrawer{
			inputting:      &inputting,
			inputMainOrAlt: &inputMainOrAlt,
			inputBuf:       &inputBuf,
		})
		window.Register(matcherEngine)
		if detectorEngine != nil {
			window.Register(detectorEngine)
		}
		window.SetBounds(capturerServer.Bounds().Max)
	}

	cwg.Go(capturerServer.Run)
	cwg.Go(matcherEngine.Run)

	if detectorEngine != nil {
		cwg.Go(func(ctx context.Context) {
			detectorEngine.Run(ctx)
		})
	}

	cwg.Go(r6sLoop)
	cwg.Go(cpuMeasureLoop)
	cwg.Go(tmplWatchLoop)

	if !*nogui {
		cwg.Go(func(ctx context.Context) {
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				window.SetScreenImage(capturerServer.ReadScreen())
				time.Sleep(time.Second / 60)
			}
		})
		cwg.Go(func(ctx context.Context) {
			window.Run(ctx)
			*nogui = true
		})
	}

	cwg.Wait()
}

func r6sLoop(ctx context.Context) {
	// the actual duration is exactly a second,
	// more for template matching loop delay
	const debounceInterval = 1500 * time.Millisecond

	lastIndex := matcher.WEAPON_INDEX_NONE
	var toNoneDebounce bool
	var lastSwitchToNone time.Time

	applyWeapon := func(newIndex int) {
		weaponsMu.RLock()
		defer weaponsMu.RUnlock()

		var to *w.Weapon
		toName := "N/A"
		if newIndex >= 0 {
			to = weapons[newIndex]
			toName = to.String()
		}

		if newIndex >= 0 {
			toNoneDebounce = false
		}

		if forceUpdate {
			forceUpdate = false
		} else if newIndex == lastIndex {
			return
		} else {
			log.Debug().
				Int("fromIndex", lastIndex).
				Int("toIndex", newIndex).
				Str("toName", toName).
				Msg("switching weapon")
		}

		// no debounce when debugging
		if !debugging && lastIndex >= 0 && newIndex == matcher.WEAPON_INDEX_NONE {
			// from notnone to none
			if !toNoneDebounce {
				// going to none, enter debounce
				toNoneDebounce = true
				lastSwitchToNone = time.Now()
				return
			} else {
				// debounce skipping
				timeToNone := lastSwitchToNone.Add(debounceInterval)
				if time.Now().Before(timeToNone) {
					log.Debug().Msg("switching skipped due to debounce")
					return
				}
				// exit debounce
				toNoneDebounce = false
			}
		}

		lastIndex = newIndex
		recoilEngine.SetWeapon(to)
	}

	ticker := time.NewTicker(time.Second / 125)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case newIndex := <-matcherEngine.ResultCh():
			applyWeapon(newIndex)
		case <-ticker.C:
			recoilEngine.Tick()
		}
	}
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

func tmplWatchLoop(ctx context.Context) {
	type myFsEvent struct {
		Name        string
		Op          fsnotify.Op
		renamedFrom string
	}
	if unsafe.Sizeof(myFsEvent{}) != unsafe.Sizeof(fsnotify.Event{}) ||
		reflect.TypeOf(myFsEvent{}).NumField() != reflect.TypeOf(fsnotify.Event{}).NumField() {
		log.Panic().Msg("[FIXME] definition of fsnotify.Event has been changed")
	}

	watcher, err := fsnotify.NewWatcher()
	panicIf(err)
	defer watcher.Close()
	panicIf(watcher.Add(TEMPLATES_DIRECTORY))

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

		time.Sleep(time.Millisecond * 100)
		err := weapons.Append(name, CREATE_MASK, MATCHING_MODE)
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
		origName, _, err := w.ParseFileName(from)
		if err == nil {
			origI = weapons.IndexByName(origName)
		}

		if origI < 0 {
			err := weapons.Append(to, CREATE_MASK, MATCHING_MODE)
			if err != nil {
				return fmt.Errorf("failed to add new weapon %q: %w", to, err)
			}
		} else {
			if strings.HasPrefix(filepath.Base(to), TEMPLATES_PREFIX_IGNORE) {
				err := weapons.Delete(origI)
				if err != nil {
					return fmt.Errorf("failed to delete %q: %w", origName, err)
				}
				return nil
			}
			return weapons[origI].DecodeFrom(to, CREATE_MASK, MATCHING_MODE)
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

			log.Debug().Any("event", event).Msg("fs event")

			switch event.Op {
			case fsnotify.Create:
				var err error
				renameFrom := (*myFsEvent)(unsafe.Pointer(&event)).renamedFrom

				if renameFrom == "" {
					err = muAddWeapon(event.Name)
				} else {
					err = muModWeapon(renameFrom, event.Name)
				}
				if err != nil {
					log.Warn().Err(err).Msg("failed to handle create/rename event")
					continue
				}

				if renameFrom == "" {
					log.Info().Str("path", event.Name).Msg("weapon added successfully")
				} else {
					log.Info().Str("path", event.Name).Str("renameFrom", renameFrom).Msg("weapon modified successfully")
				}

			case fsnotify.Remove:
				deleted, err := muDelWeapon(event)
				if err != nil {
					log.Warn().Err(err).Msg("failed to handle remove event")
					continue
				}

				switch deleted {
				case 1:
					log.Info().Str("path", event.Name).Msg("weapon deleted successfully")
				case 0:
					log.Warn().Str("path", event.Name).Msg("weapon failed to delete")
				default:
					log.Warn().Str("path", event.Name).Int("deleted", deleted).Msg("weapon triggered multiple deletions")
				}
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Error().Err(err).Msg("fsnotify error")
		}
	}
}

func initDetector() *detector.Engine {
	cfg := detector.DefaultConfig()
	cfg.Fps = 10

	log.Debug().
		Str("modelPath", cfg.ModelPath).
		Str("onnxLibPath", cfg.OnnxLibPath).
		Float32("confThresh", cfg.ConfThresh).
		Int("inputSize", cfg.InputSize).
		Bool("useCuda", cfg.UseCuda).
		Bool("useTensorRT", cfg.UseTensorRT).
		Str("tensorRTPluginPath", cfg.TensorRTPluginPath).
		Msg("initializing detector engine")

	engine, err := detector.New(capturerServer, cfg)
	if err != nil {
		log.Error().Err(err).Msg("failed to init person engine")
		return nil
	}

	log.Info().Msg("person engine initialized")
	return engine
}

var (
	showPosTill           time.Time
	inputting             bool
	inputMainOrAlt        bool
	inputBuf              bytes.Buffer
	weaponNameLongest     int
	onceWeaponNameLongest sync.Once
)

type metricsDrawer struct {
	inputting      *bool
	inputMainOrAlt *bool
	inputBuf       *bytes.Buffer
}

func (d *metricsDrawer) Draw(gtx layout.Context, s ui.DScale) {
	m := snapshotMetrics()

	var sb strings.Builder

	fmt.Fprintf(&sb, "| Capture: %.0ffps(%.1fms)", m.CaptureFps, m.CaptureCostMs)

	fmt.Fprintf(&sb, " | Match: %.0ffps(%.1fms/%d=%.2fms)", m.MatchFps, m.MatchCostMs, m.MatchCount, safeDiv(m.MatchCostMs, float64(m.MatchCount)))

	if detectorEngine != nil {
		fmt.Fprintf(&sb, " | Detection: %.0ffps/%d(%.1fms) |", m.DetectionFps, m.DetectionCount, m.DetectionCostMs)
	}
	sb.WriteByte('\n')

	fmt.Fprintf(&sb, "| 0x%04X | CPU: %04.1f%% | GC: %d(avg: %.2fus, last: %.2fs) |", m.FramesElapsed, cpu, m.GcCount, m.GcPauseAvgUs, m.GcSinceLastS)
	if debugging {
		sb.WriteString(" DEBUG |")
	}
	sb.WriteByte('\n')

	if *d.inputting {
		if !*d.inputMainOrAlt {
			sb.WriteString("|M|: ")
		} else {
			sb.WriteString("|A|: ")
		}
		sb.Write(d.inputBuf.Bytes())
		sb.WriteByte('\n')
	}

	if recoilEngine != nil {
		wp := recoilEngine.Weapon()
		recoilEngine.DisplayState(&sb)
		sb.WriteByte('\n')
		wp.DisplaySpeed(&sb, debugging)
		sb.WriteByte('\n')
	}

	ui.DrawList(gtx, strings.Split(strings.TrimSpace(sb.String()), "\n"))
}

func safeDiv(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

func createShortcuts(receiver any) widgets.Shortcuts {
	if *nogui || receiver == nil {
		return widgets.Shortcuts{}
	}

	return widgets.NewShortcuts(receiver,
		widgets.NewShortcut(key.NameEscape).
			Do(func(_ key.Name, _ key.Modifiers) {
				*nogui = true
				wndTitle, _ := syscall.UTF16PtrFromString(windowTitle)
				hwnd := user32FindWindow(0, wndTitle)
				if hwnd != 0 {
					user32PostMessage(hwnd, 0x0010, 0, 0)
				}
			}),

		widgets.NewShortcut(key.NameSpace).
			Do(func(_ key.Name, _ key.Modifiers) {
				listWeapons()
			}),

		widgets.NewShortcut("W", "w").
			Do(func(_ key.Name, mod key.Modifiers) {
				if mod.Contain(key.ModCtrl | key.ModShift) {
					loadTemplates()
				}
			}),

		widgets.NewShortcut("P", "p").
			Do(func(_ key.Name, _ key.Modifiers) {
				windowHandel = windows.GetForegroundWindow()
				log.Info().
					Int("parentProcessId", parentProcessId).
					Int("processId", processId).
					Uint64("windowHandel", uint64(windowHandel)).
					Msg("process/window info")
			}),

		widgets.NewShortcut("F", "f").
			Do(func(_ key.Name, _ key.Modifiers) {
				capturerServer.ResetFramesElapsed()
				log.Info().Msg("capturer.FramesElapsed reset")
			}),

		widgets.NewShortcut("D", "d").
			Do(func(_ key.Name, _ key.Modifiers) {
				if window != nil {
					window.SetDrawEnabled(!window.DrawEnabled())
				}
			}),

		widgets.NewShortcut("B", "b").
			Do(func(_ key.Name, _ key.Modifiers) {
				debugging = !debugging
				forceUpdate = true
				log.Info().Bool("debugging", debugging).Msg("debugging toggled")
			}),

		widgets.NewShortcut("R", "r",
			key.NameUpArrow, key.NameDownArrow,
			key.NameLeftArrow, key.NameRightArrow).
			Do(func(n key.Name, mod key.Modifiers) {
				moveROI(n, mod)
			}),

		widgets.NewShortcut("T", "t").
			Do(func(_ key.Name, mod key.Modifiers) {
				toggleWDA(mod)
			}),

		widgets.NewShortcut("I", "i",
			"0", "1", "2", "3", "4",
			"5", "6", "7", "8", "9",
			".", "-", key.NameReturn,
			key.NameDeleteBackward).
			Do(func(k key.Name, m key.Modifiers) {
				handleInput(k, m)
			}),
	)
}

func moveROI(name key.Name, mod key.Modifiers) {
	boundaryCheck := func(constraints image.Rectangle, rect *image.Rectangle) {
		boundaryCheckPos := func(constraints image.Rectangle, pos *image.Point) {
			pos.X = max(pos.X, constraints.Min.X)
			pos.Y = max(pos.Y, constraints.Min.Y)
			pos.X = min(pos.X, constraints.Max.X)
			pos.Y = min(pos.Y, constraints.Max.Y)
		}
		size := rect.Size()
		boundaryCheckPos(constraints, &rect.Max)
		rect.Min = rect.Max.Sub(size)
		boundaryCheckPos(constraints, &rect.Min)
		rect.Max = rect.Min.Add(size)
	}

	offset := 1
	for range bits.OnesCount32(uint32(mod)) {
		offset *= 4
	}

	var newRect image.Rectangle
	switch name {
	case "R", "r":
		newRect = image.Rectangle{roiRectPos, roiRectPos.Add(roiRectSize)}
	case key.NameUpArrow:
		newRect = roiRect.Sub(image.Pt(0, offset))
	case key.NameDownArrow:
		newRect = roiRect.Add(image.Pt(0, offset))
	case key.NameLeftArrow:
		newRect = roiRect.Sub(image.Pt(offset, 0))
	case key.NameRightArrow:
		newRect = roiRect.Add(image.Pt(offset, 0))
	}

	boundaryCheck(capturerServer.Bounds(), &newRect)
	roiRect = newRect
	matcherEngine.SetRoi(newRect)
	showPosTill = time.Now().Add(time.Second * 3)
	log.Debug().Any("roiRect", roiRect).Msg("roiRect moved")
}

func toggleWDA(mod key.Modifiers) {
	if windowHandel == 0 {
		windowHandel = windows.GetForegroundWindow()
	}

	currWda, err := GetWindowDisplayAffinity(windowHandel)
	if err != nil {
		log.Error().Err(err).Msg("failed to GetWindowDisplayAffinity")
		return
	}

	switch currWda {
	case WDA_NONE:
		var toWda uint32
		if !mod.Contain(key.ModShift) {
			toWda = WDA_EXCLUDEFROMCAPTURE
			log.Info().Msg("wda set to WDA_EXCLUDEFROMCAPTURE")
		} else {
			toWda = WDA_MONITOR
			log.Info().Msg("wda set to WDA_MONITOR")
		}
		err = SetWindowDisplayAffinity(windowHandel, toWda)
	case WDA_EXCLUDEFROMCAPTURE:
		log.Info().Msg("wda set to WDA_NONE")
		err = SetWindowDisplayAffinity(windowHandel, WDA_NONE)
	}

	if err != nil {
		log.Error().Err(err).Msg("failed to SetWindowDisplayAffinity")
	}
}

func handleInput(k key.Name, m key.Modifiers) {
	switch k {
	case "I", "i":
		if !debugging {
			log.Warn().Msg("not in debugging")
			return
		}
		inputting = true
		inputMainOrAlt = m.Contain(key.ModShift)
		return

	case key.NameDeleteBackward:
		if inputBuf.Len() == 0 {
			return
		}
		inputBuf.Truncate(inputBuf.Len() - 1)

	case key.NameReturn:
		if !inputting {
			return
		}
		inputting = false
		if inputBuf.Len() == 0 {
			log.Warn().Msg("empty input")
			return
		}
		modWeapon(inputMainOrAlt, inputBuf.String())
		inputBuf.Reset()

	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", ".", "-":
		if inputting {
			inputBuf.WriteString(string(k))
		}
		return
	}
}

func modWeapon(mainOrAlt bool, newSpeed string) {
	idx := matcherEngine.WeaponIndex()
	if idx == matcher.WEAPON_INDEX_NONE {
		log.Warn().Msg("weapon unselected")
		return
	}

	switch newSpeed {
	case "-":
		// 切换到none时的防抖
		mainOrAlt = true
		newSpeed = w.SPEED_SIGN_AUTO
	case "--":
		mainOrAlt = true
		newSpeed = w.SPEED_SIGN_COPY
	}

	weaponsMu.RLock()
	wps := matcherEngine.Weapons()
	orig := wps[idx]
	weaponsMu.RUnlock()

	dir := filepath.Dir(orig.Path)
	origName := filepath.Base(orig.Path)
	ext := filepath.Ext(origName)

	var speedMain, speedAlt string
	if !mainOrAlt {
		speedMain = newSpeed
		if orig.SpeedAltFrac != 0 {
			speedAlt = fmt.Sprintf("%d.%d", orig.SpeedAltInt, orig.SpeedAltFrac)
		} else {
			speedAlt = fmt.Sprintf("%d", orig.SpeedAltInt)
		}
	} else {
		if orig.SpeedMainFrac != 0 {
			speedMain = fmt.Sprintf("%d.%d", orig.SpeedMainInt, orig.SpeedMainFrac)
		} else {
			speedMain = fmt.Sprintf("%d", orig.SpeedMainInt)
		}
		speedAlt = newSpeed
	}

	newName := fmt.Sprintf("{%s_%s_%s} %s%s",
		orig.Class.ToString(true),
		speedMain, speedAlt,
		orig.Name, ext,
	)
	newPath := filepath.Join(dir, newName)
	err := os.Rename(orig.Path, newPath)
	if err != nil {
		log.Error().Err(err).
			Str("from", orig.Path).
			Str("to", newPath).
			Msg("failed to rename weapon file")
	} else {
		log.Info().
			Str("from", orig.Path).
			Str("to", newPath).
			Msg("renamed weapon file")
	}

	forceUpdate = true
}

func listWeapons() {
	const indexLength = 3

	weaponsMu.RLock()
	wps := matcherEngine.Weapons()
	weaponsMu.RUnlock()

	onceWeaponNameLongest.Do(func() {
		for _, w := range wps {
			weaponNameLongest = max(weaponNameLongest, len(w.Name))
		}
	})

	skipped := 0
	for i, w := range wps {
		if w.SpeedMain == 0 {
			skipped++
			continue
		}

		speedMain, speedMainF, speedAlt, speedAltF := w.GetAllSpeeds(debugging)
		fmt.Fprintf(os.Stdout,
			"[%0*d] {%s_%02d.%d_%02d.%d} %-*s %.2f%%\n",
			indexLength, i,
			w.Class.ToString(true),
			speedMain, speedMainF, speedAlt, speedAltF,
			weaponNameLongest, w.Name,
			w.Template.MaxVal*100,
		)
	}
	if skipped > 0 {
		log.Info().Int("skipped", skipped).Msg("skipped undefined weapon(s)")
	}
}

var (
	user32          = syscall.MustLoadDLL("user32.dll")
	procFindWindow  = user32.MustFindProc("FindWindowW")
	procPostMessage = user32.MustFindProc("PostMessageW")
)

func user32FindWindow(className uintptr, windowName *uint16) uintptr {
	hwnd, _, _ := procFindWindow.Call(className, uintptr(unsafe.Pointer(windowName)))
	return hwnd
}

func user32PostMessage(hwnd uintptr, msg uint32, wParam, lParam uintptr) {
	procPostMessage.Call(hwnd, uintptr(msg), wParam, lParam)
}
