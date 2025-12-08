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
	"github.com/Miuzarte/GoCVStreamer/fps"
	"github.com/Miuzarte/GoCVStreamer/template"

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

var (
	colorCoral  = color.RGBA{0xA6, 0x62, 0x61, 0}
	colorRed    = color.RGBA{0xFF, 0, 0, 0}
	colorYellow = color.RGBA{0xFF, 0xFF, 0, 0}
	colorGreen  = color.RGBA{0, 0xFF, 0, 0}
	colorCyan   = color.RGBA{0, 0xFF, 0xFF, 0}
	colorBlue   = color.RGBA{0, 0, 0xFF, 0}
	colorPurple = color.RGBA{0xFF, 0, 0xFF, 0}
)

const (
	fontSize        = 1
	fontThickness   = 2 * fontSize
	fontWidth       = 20 * fontSize
	fontHeight      = 24 * fontSize
	fontSpacingX    = 4 * fontThickness
	fontSpacingY    = 4 * fontThickness
	borderThickness = 2
)

var (
	parentProcessId = os.Getppid()
	processId       = os.Getpid()
	highGuiHandel   windows.HWND
	windowTitle     = strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
)

var (
	displayIndex = 0 // [TODO]
	capturer     *capture.Capturer
	screenImage  *image.RGBA
)

var (
	roiRect = image.Rect(1986+8, 1197+8, 2114-16, 1306-8)
	weapons Weapons
)

// [TODO]? dynamic threshold
const matchThreshold = 0.9

var (
	captureCost           time.Duration
	templatesMatchingCost time.Duration
	drawCost              time.Duration
	imShowCost            time.Duration
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

	highGui := gocv.NewWindow(windowTitle)
	defer highGui.Close()
	highGui.ResizeWindow(1280, 720)
	display := gocv.NewMat()
	defer display.Close()

	ticker := time.NewTicker(time.Millisecond * 128)
	defer ticker.Stop()

	outputSignal := make(chan int, 1)
	go func() {
		for weaponIndex := range outputSignal {
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
	}()

	go runGio(ctx)

LOOP:
	for {
		select {
		case <-ctx.Done():
			break LOOP

		case <-ticker.C:
			// 窗口关闭
			windowProp := highGui.GetWindowProperty(gocv.WindowPropertyVisible)
			switch {
			case windowProp < 0:
				log.Error("unexpected window property: %d", windowProp)
				fallthrough
			case windowProp == 0:
				cancel()
				continue
			}

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
			tmplIndex, tmplMatched, found := doMatchWeapon(captureRoi)
			templatesMatchingCost = time.Since(tStart)
			captureRoi.Close()

			// output
			if found {
				outputSignal <- tmplIndex
			} else {
				outputSignal <- -1
			}

			// draw
			tStart = time.Now()
			doDraw(&display, tmplIndex, tmplMatched, found)
			drawCost = time.Since(tStart)

			// show
			tStart = time.Now()
			panicIf(highGui.IMShow(display))
			key := highGui.PollKey()
			imShowCost = time.Since(tStart)
			if key != -1 {
				if key < 128 {
					log.Debugf("key: %d %s", key, string(key))
				} else {
					log.Debugf("key: %d", key)
				}
				switch key {
				case ' ':
					for i, tmpl := range weapons {
						fmt.Printf("[%d] %s %.2f%%\n", i, tmpl.Name, tmpl.Template.MaxVal*100)
					}
				case 'p', 'P':
					highGuiHandel = windows.GetForegroundWindow()
					log.Infof("process: %#X", highGuiHandel)
				case 'r', 'R':
					capturer.FramesElapsed = 0
					log.Info("screenshooter.FramesElapsed reset")
				}
			}

			// to gioui
			gioDisplay, err = display.ToImage()
			if err != nil {
				log.Warnf("failed to convert mat to image for gioui: %v", err)
			}
			gioWindow.Invalidate()

		}
	}

	wg.Wait()
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
	return imageToMat(screenImage, display)
}

const method = gocv.TmCcoeffNormed

var lastSuccessfulTempl int

func doMatchWeapon(captureRoi gocv.Mat) (templateIndex, templateMatched int, found bool) {
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

const fpsMaxHistoryLen = 30

var fpsCounter = fps.NewCounter(time.Second / 2)

func doDraw(display *gocv.Mat, tmplIndex, tmplMatched int, found bool) {
	gocv.Rectangle(display,
		roiRect,
		colorCoral, borderThickness,
	)
	gocv.PutText(display,
		"ROI",
		image.Pt(roiRect.Min.X, roiRect.Min.Y-fontSpacingY),
		gocv.FontHersheyDuplex, fontSize,
		colorCoral, fontThickness,
	)
	gocv.PutText(display,
		fmt.Sprintf(
			"| FPS: %.2f | SSC: %dms | TMC: %dms/%d=%dms | ISC: %dms | %d |",
			fpsCounter.Count(),
			captureCost/time.Millisecond,
			templatesMatchingCost/time.Millisecond, tmplMatched,
			templatesMatchingCost/time.Duration(tmplMatched)/time.Millisecond,
			imShowCost/time.Millisecond,
			capturer.FramesElapsed,
		),
		image.Pt(10, 60),
		gocv.FontHersheyDuplex, 2,
		colorCoral, 4,
	)

	colorPos := colorGreen
	colorNeg := colorCyan
	min, max := weapons.MinMaxIndex()
	var weaponPos *Weapon
	weaponNeg := weapons[min]
	if found {
		weaponPos = weapons[tmplIndex]
	} else {
		weaponPos = weapons[max]
		// 黄框显示最高匹配的模板
		colorPos = colorYellow
	}

	if weaponPos.Template.MaxVal > 0 {
		drawResultRect(display, colorPos, &weaponPos.Template)
		drawTextRight(display, colorPos, 0, weaponPos.Name)
		drawTextRight(display, colorPos, 1, fmt.Sprintf("%.2f%%", weaponPos.Template.MaxVal*100))
		tmplPosRect := image.Rect( // 匹配的模板本身
			roiRect.Min.X, // 与ROI左对齐
			roiRect.Max.Y+borderThickness,
			roiRect.Min.X+weaponPos.Template.Width,
			roiRect.Max.Y+borderThickness+weaponPos.Template.Height,
		)
		drawTemplate(display, tmplPosRect, &weaponPos.Template)

		if drawNeg {
			drawResultRect(display, colorNeg, &weaponNeg.Template)
			drawTextRight(display, colorNeg, 3, weaponNeg.Name)
			drawTextRight(display, colorNeg, 4, fmt.Sprintf("%.2f%%", weaponNeg.Template.MaxVal*100))
			tmplNegRect := image.Rect(
				roiRect.Min.X,
				tmplPosRect.Max.Y,
				roiRect.Min.X+weaponNeg.Template.Width,
				tmplPosRect.Max.Y+weaponNeg.Template.Height,
			)
			drawTemplate(display, tmplNegRect, &weaponNeg.Template)
		}

	}
}

func drawResultRect(display *gocv.Mat, color color.RGBA, tmpl *template.Template) {
	resultX := tmpl.MaxLoc.X + roiRect.Min.X
	resultY := tmpl.MaxLoc.Y + roiRect.Min.Y
	rect := image.Rect(
		resultX,
		resultY,
		resultX+tmpl.Width,
		resultY+tmpl.Height,
	)
	gocv.Rectangle(display,
		rect,
		color, borderThickness,
	)
}

func drawTextRight(display *gocv.Mat, color color.RGBA, line int, texts string) {
	textX := roiRect.Max.X + fontSpacingX
	textY := roiRect.Min.Y + fontHeight + (line * (fontHeight + fontSpacingY))
	gocv.PutText(display,
		texts,
		image.Pt(textX, textY),
		gocv.FontHersheyDuplex, fontSize,
		color, fontThickness,
	)
}

func drawTemplate(display *gocv.Mat, rect image.Rectangle, tmpl *template.Template) {
	region := display.Region(rect)
	defer region.Close()

	channels := display.Channels()

	if tmpl.Channels == 3 && channels == 3 {
		tmpl.Mat.CopyTo(&region)
	} else if tmpl.Channels == 1 && channels == 3 {
		colorTemplate := gocv.NewMat()
		defer colorTemplate.Close()
		gocv.CvtColor(tmpl.Mat, &colorTemplate, gocv.ColorGrayToBGR)
		colorTemplate.CopyTo(&region)
	} else {
		log.Warnf("mismatched channels of template and display: %d, %d", tmpl.Channels, channels)
	}
}
