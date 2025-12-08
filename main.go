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

	"github.com/Miuzarte/GoCVStreamer/capture"

	"github.com/Miuzarte/SimpleLog"
	"github.com/kbinani/screenshot"
	"github.com/kirides/go-d3d/outputduplication"
	"gocv.io/x/gocv"
	"golang.org/x/sys/windows"
)

const (
	debug   = false
	drawNeg = false
)

const (
	KEY_ESC = 27
)

const (
	templatesDir = "templates"
)

var log = SimpleLog.New("[Streamer]", true, false).SetLevel(SimpleLog.DebugLevel)

type rgba struct {
	R, G, B, A uint8
}

var (
	colorWhite = rgba{0xFF, 0xFF, 0xFF, 0xFF}
	colorBlack = rgba{0x00, 0x00, 0x00, 0xFF}

	colorCoral = rgba{0xA6, 0x62, 0x61, 0xFF}

	colorRed    = rgba{0xFF, 0x00, 0x00, 0xFF}
	colorYellow = rgba{0xFF, 0xFF, 0x00, 0xFF}
	colorGreen  = rgba{0x00, 0xFF, 0x00, 0xFF}
	colorCyan   = rgba{0x00, 0xFF, 0xFF, 0xFF}
	colorBlue   = rgba{0x00, 0x00, 0xFF, 0xFF}
	colorPurple = rgba{0xFF, 0x00, 0xFF, 0xFF}
)

var (
	parentProcessId = os.Getppid()
	processId       = os.Getpid()
	windowHandel    windows.HWND
	windowTitle     = strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
)

var (
	displayIndex = 0 // [TODO] 自动取分辨率最高的屏幕
	capturer     *capture.Capturer
	screenImage  *image.RGBA
	roiRect      = image.Rect(1986+8, 1197+8, 2114-16, 1306-8)
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
		panicIf(fmt.Errorf("failed to initialize window name"))
	}

	numDisplays := screenshot.NumActiveDisplays()
	log.Infof("num displays: %d", numDisplays)
	switch {
	case numDisplays == 0:
		panicIf(fmt.Errorf("display not found"))
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
	// [TODO] OpenCV单独开协程并锁定线程, 仅该协程存在C调用
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	defer func() {
		panicIf(weapons.Close())
	}()

	wg := sync.WaitGroup{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, _ = signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)

	display := gocv.NewMat()
	defer display.Close()

	ticker := time.NewTicker(time.Millisecond * 128)
	defer ticker.Stop()

	outputSignal := make(chan int, 1)
	go outputLua(outputSignal)

	go func() {
		defer cancel()
		runGioui(ctx)
	}()

LOOP:
	for {
		select {
		case <-ctx.Done():
			break LOOP

		case <-ticker.C:
			// screenshot
			tStart := time.Now()
			err := doScreenshot(screenImage, &display)
			captureCost = time.Since(tStart)
			if err == outputduplication.ErrNoImageYet {
				continue
			}
			panicIf(err)

			// template match
			captureRoi := display.Region(roiRect)
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

			gioDisplay, err = display.ToImage()
			if err != nil {
				log.Warnf("failed to convert mat to image for gioui: %v", err)
			} else {
				window.Invalidate()
			}

		}
	}

	wg.Wait()
}

func outputLua(sig chan int) {
	for weaponIndex := range sig {
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
