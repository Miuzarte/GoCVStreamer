package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"gioui.org/app"
	"gioui.org/op"

	cwg "github.com/Miuzarte/GoCVStreamer/contextWaitGroup"
	"github.com/Miuzarte/GoCVStreamer/fps"
	"github.com/Miuzarte/GoCVStreamer/logger"
	w "github.com/Miuzarte/GoCVStreamer/weapon"
	ws "github.com/Miuzarte/GoCVStreamer/weapons"
	"github.com/fsnotify/fsnotify"

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

var (
	nogui    = flag.Bool("nogui", false, "run without GUI window")
	httpPort = flag.String("port", ":8080", "HTTP metrics server port")
	nohttp   = flag.Bool("nohttp", false, "disable HTTP metrics server")
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

const (
	SAMPLE_RATE                  = 5 // Hz
	SAMPLE_INTERVAL              = time.Second / SAMPLE_RATE
	SAMPLE_RATE_IDLE             = 2 // Hz
	SAMPLE_INTERVAL_IDLE         = time.Second / SAMPLE_RATE_IDLE
	SAMPLE_RATE_TO_IDLE_DURATION = time.Second * 5
)

var (
	parentProcessId = os.Getppid()
	processId       = os.Getpid()
	windowHandel    windows.HWND
	windowTitle     = strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")

	processSelf *process.Process
)

var (
	drawEnabled = true
	roiRectSize = image.Point{8 * 11, 8 * 13}
	roiRectPos  = image.Point{2000, 1200}
	// Xmid: 2045
	defaultRoiRect = image.Rectangle{roiRectPos, roiRectPos.Add(roiRectSize)}
	roiRect        = defaultRoiRect
)

var (
	weaponsMu      sync.RWMutex
	weapons        ws.Weapons
	weaponIndex    int
	weaponsMatched int
	weaponFound    bool
)

const WEAPON_INDEX_NONE = -1

const MATCH_THRESHOLD = 0.9

var (
	lastGCStats         debug.GCStats
	captureCost         time.Duration
	weaponsMatchingCost time.Duration
	fpsCount            float64
	fpsFrametime        time.Duration
	highLatencyCount    int
	lastHighLatencyTime time.Time
)

var fpsCounter = fps.NewCounter(SAMPLE_INTERVAL)

var (
	inIdle              = false
	narrowing           = false
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

	if !*nogui {
		if windowTitle == "" {
			log.Panic().
				Msg("failed to initialize window name")
		}
		window.Option(
			app.Title(windowTitle),
			app.MinSize(1280, 720),
			app.Size(1280, 720),
		)
	}

	err = windows.SetPriorityClass(windows.CurrentProcess(), windows.HIGH_PRIORITY_CLASS)
	if err != nil {
		log.Warn().
			Err(err).
			Msg("failed to set process priority")
	}

	processSelf, err = process.NewProcess(int32(processId))
	if err != nil {
		log.Panic().
			Err(err).
			Msg("failed to get process")
	}

	selectDisplay()

	loadTemplates()

	luaFile, err = os.OpenFile("speed.lua", os.O_WRONLY|os.O_CREATE, 0o664)
	if err != nil {
		log.Panic().
			Err(err).
			Msg("failed to open file")
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

		var err error
		err = capturer.Close()
		if err != nil {
			log.Error().
				Err(err).
				Msg("failed to close capturer")
		}
		err = weapons.Close()
		if err != nil {
			log.Error().
				Err(err).
				Msg("failed to close weapons")
		}
		err = luaFile.Close()
		if err != nil {
			log.Error().
				Err(err).
				Msg("failed to close luaFile")
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

	cwg.Go(func(ctx context.Context) {
		cpuMeasureLoop(ctx)
	})
	cwg.Go(func(ctx context.Context) {
		luaSwitchingLoop(ctx)
	})
	cwg.Go(func(ctx context.Context) {
		tmplWatchLoop(ctx)
	})
	cwg.Go(func(ctx context.Context) {
		tmplMatchLoop(ctx)
	})
	if !*nogui {
		cwg.Go(func(ctx context.Context) {
			defer cwg.Cancel()
			windowLoop(ctx)
		})
		cwg.Go(func(ctx context.Context) {
			<-ctx.Done()
			window.Invalidate()
		})
	}

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

		var from *w.Weapon
		fromName := "N/A"
		var fromVal float32
		if luaFileContentIndex >= 0 {
			from = weapons[luaFileContentIndex]
			fromName = from.String()
			fromVal = from.Template.MaxVal
		}

		var to *w.Weapon
		toName := "N/A"
		var toVal float32
		if newIndex >= 0 {
			to = weapons[newIndex]
			toName = to.String()
			toVal = to.Template.MaxVal
		}

		if newIndex >= 0 {
			luaToNoneDebounce = false
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
			log.Debug().
				Int("fromIndex", luaFileContentIndex).
				Int("toIndex", newIndex).
				Str("fromName", fromName).
				Str("toName", toName).
				Float32("fromVal", fromVal).
				Float32("toVal", toVal).
				Msg("switching weapon")
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
					log.Debug().Msg("switching skipped due to debounce")
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
				log.Error().Err(e.Err).Msg("window error")
			} else {
				log.Debug().Msg("window closed normally")
			}
			return

		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			dScale = gtx.Metric

			err := shortcuts.Match(gtx)
			if err != nil {
				log.Warn().Err(err).Msg("shortcuts match error")
			}

			if screenImage != nil {
				layoutDisplay(gtx, screenImage)
			}
			if drawEnabled {
				layoutMetrics(gtx)
			}

			e.Frame(gtx.Ops)

		case app.ConfigEvent:
		// case app.wakeupEvent:
		default:
			log.Trace().
				Str("eventType", fmt.Sprintf("%T", e)).
				Any("event", e).
				Msg("window event")
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
			// } else {
			// 	ignore
		}

		if origI < 0 {
			// load the new one
			err := weapons.Append(to, CREATE_MASK, MATCHING_MODE)
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

			log.Debug().
				Any("event", event).
				Msg("fs event")

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
					log.Warn().Err(err).Msg("failed to handle create/rename event")
					continue
				}

				if renameFrom == "" {
					log.Info().
						Str("path", event.Name).
						Msg("weapon added successfully")
				} else {
					log.Info().
						Str("path", event.Name).
						Str("renameFrom", renameFrom).
						Msg("weapon modified successfully")
				}

			case fsnotify.Remove:
				deleted, err := muDelWeapon(event)
				if err != nil {
					log.Warn().Err(err).Msg("failed to handle remove event")
					continue
				}

				switch deleted {
				case 1:
					log.Info().
						Str("path", event.Name).
						Msg("weapon deleted successfully")
				case 0:
					log.Warn().
						Str("path", event.Name).
						Msg("weapon failed to delete")
				default:
					log.Warn().
						Str("path", event.Name).
						Int("deleted", deleted).
						Msg("weapon triggered multiple deletions")
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

func tmplMatchLoop(ctx context.Context) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	capture := gocv.NewMat()
	defer capture.Close()

	tickerNormal := time.NewTicker(SAMPLE_INTERVAL)
	defer tickerNormal.Stop()
	tickerIdle := time.NewTicker(SAMPLE_INTERVAL_IDLE)
	defer tickerIdle.Stop()
	ticker := make(chan time.Time, 2)
	defer close(ticker)

	lastFoundTime := time.Now()
	narrowing = false
	lastSlot := w.Slot(w.SLOT_UNDEFINED)

	for {
		select {
		case <-ctx.Done():
			return

		case t, ok := <-tickerNormal.C:
			if !ok {
				return
			}
			if inIdle {
				continue
			}
			select {
			case ticker <- t:
			default:
			}
		case t, ok := <-tickerIdle.C:
			if !ok {
				return
			}
			if !inIdle {
				continue
			}
			select {
			case ticker <- t:
			default:
			}

		case _, ok := <-ticker:
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
				log.Error().
					Any("roiRect", roiRect).
					Any("screenBounds", capturer.Bounds()).
					Msg("roiRect is not fully contained in screen bounds")
				continue
			}
			// template match
			captureRoi := capture.Region(roiRect)
			tStart = time.Now()
			slotFilter := w.Slot(w.SLOT_UNDEFINED)
			if narrowing {
				slotFilter = lastSlot.Opposite()
			}
			weaponIndex, weaponsMatched, weaponFound = doMatchWeapon(captureRoi, slotFilter)
			weaponsMatchingCost = time.Since(tStart)
			captureRoi.Close()

			fpsCount, fpsFrametime = fpsCounter.Count()

			// output
			if weaponFound {
				// exit idle
				lastFoundTime = time.Now()
				inIdle = false
				lastSlot = weapons[weaponIndex].Class.Detail().Slot
				if lastSlot != w.SLOT_UNDEFINED && !lastSlot.Is(w.SLOT_MIX) {
					narrowing = true
				} else {
					narrowing = false
				}
				weaponIndexSignal <- weaponIndex
			} else {
				// failed for a period of time,
				// reduce performance consumption in idle state
				// no idle when debugging
				if time.Since(lastFoundTime) > SAMPLE_RATE_TO_IDLE_DURATION && !debugging {
					inIdle = true
					narrowing = false
					lastSlot = w.SLOT_UNDEFINED
				}
				weaponIndexSignal <- WEAPON_INDEX_NONE
			}

			if !*nogui {
				window.Invalidate()
			}
		}
	}
}

var lastSuccessfulTempl int

func doMatchWeapon(image gocv.Mat, slotFilter w.Slot) (templateIndex, templateMatched int, found bool) {
	weaponsMu.RLock()
	defer weaponsMu.RUnlock()

	const method = gocv.TmCcoeffNormed

	for j := range weapons {
		i := j + lastSuccessfulTempl // 从上次成功的模板开始往下匹配
		i %= len(weapons)
		templateIndex = i

		tmpl := weapons[i]

		if slotFilter != w.SLOT_UNDEFINED && j != 0 && !tmpl.Class.Detail().Slot.Has(slotFilter) {
			continue
		}

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
