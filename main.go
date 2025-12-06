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
	KEY_ESC = 27
)

const (
	debug   = false
	drawNeg = false
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
	process       windows.HWND
	windowName    = strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	displayIndex  = 0 // [TODO]
	screenshooter *capture.Screenshooter
	screenImage   *image.RGBA
	roiRect       = image.Rect(1986+8, 1197+8, 2114-16, 1306-8)
)

var (
	templates       Templates
	templateResults TemplateResults
)

// [TODO]? dynamic threshold
const matchThreshold = 0.9

var (
	screenshotCost        time.Duration
	templatesMatchingCost time.Duration
	drawCost              time.Duration
	imShowCost            time.Duration
)

var (
	luaFile      *os.File
	luaFileIndex = -1
)

var _ = wiatForInput()

func wiatForInput() (_ struct{}) {
	if !debug {
		return
	}
	log.Info("waiting for any input...")
	_, err := bufio.NewReader(os.Stdin).ReadBytes('\n')
	if err == io.EOF {
		err = nil
	}
	panicIf(err)
	return
}

func init() {
	var err error

	err = windows.SetPriorityClass(windows.CurrentProcess(), windows.HIGH_PRIORITY_CLASS)
	if err != nil {
		log.Warnf("failed to set process priority: %v", err)
	}

	if windowName == "" {
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

	screenshooter, err = capture.New(displayIndex)
	panicIf(err)
	log.Infof("display bounds: %dx%d", screenshooter.Bounds().Dx(), screenshooter.Bounds().Dy())
	screenImage = image.NewRGBA(screenshooter.Bounds())

	// load template
	panicIf(templates.IMReadDir("templates", 1, false))
	log.Infof("templates loaded: %d", len(templates))
	templateResults = make(TemplateResults, len(templates))
	for i := range templateResults {
		templateResults[i].Mat = gocv.NewMat()
	}
}

func main() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	defer func() {
		for _, mat := range templateResults {
			if !mat.Closed() && !mat.Empty() {
				panicIf(mat.Close())
			}
		}
		panicIf(templates.CloseAll())
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, _ = signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)

	window := gocv.NewWindow(windowName)
	defer window.Close()
	window.ResizeWindow(1280, 720)
	display := gocv.NewMat()
	defer display.Close()

	var err error
	luaFile, err = os.OpenFile("speed.lua", os.O_WRONLY|os.O_CREATE, 0o664)
	panicIf(err)

	ticker := time.NewTicker(time.Millisecond * 128)
	defer ticker.Stop()

	outputSignal := make(chan int, 1)
	go func() {
		for templateIndex := range outputSignal {
			if templateIndex == luaFileIndex {
				continue
			}

			if luaFileIndex >= 0 && templateIndex >= 0 {
				os.Stderr.Write([]byte{'\a'})
			}
			if luaFileIndex >= 0 {
				fromTmpl := templates[luaFileIndex].Name
				var toTmpl string
				if templateIndex >= 0 {
					toTmpl = templates[templateIndex].Name
				}
				log.Debugf(
					"switch from %d(%s) to %d(%s), last maxVal: %.2f",
					luaFileIndex, fromTmpl,
					templateIndex, toTmpl,
					templateResults[luaFileIndex].MaxVal*100,
				)
			}
			luaFileIndex = templateIndex
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
			if templateIndex < 0 { // not found, all zero
				content = "FAA=0\nFA1=0\nSAA=0\nSA1=0"
			} else {
				tmpl := templates[templateIndex]
				switch tmpl.Mode {
				case TEMPLATE_MODE_FA:
					content = "FAA=" + tmpl.SpeedAcog +
						"\n" + "FA1=" + tmpl.Speed1x +
						"\n" + "SAA=0" + "\n" + "SA1=0"
				case TEMPLATE_MODE_SA:
					content = "FAA=0" + "\n" + "FA1=0" +
						"\n" + "SAA=" + tmpl.SpeedAcog +
						"\n" + "SA1=" + tmpl.Speed1x
				default:
					log.Warnf("unexpected tmpl.Mode: %s", tmpl.Mode)
					continue
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

LOOP:
	for {
		select {
		case <-ctx.Done():
			break LOOP

		case <-ticker.C:
			// 窗口关闭
			windowProp := window.GetWindowProperty(gocv.WindowPropertyVisible)
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
			screenshotCost = time.Since(tStart)
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
			panicIf(window.IMShow(display))
			key := window.PollKey()
			imShowCost = time.Since(tStart)
			if key != -1 {
				if key < 128 {
					log.Debugf("key: %d %s", key, string(key))
				} else {
					log.Debugf("key: %d", key)
				}
				switch key {
				case ' ':
					for i := range templates {
						fmt.Printf("[%d] %s %.2f%%\n", i, templates[i].Name, templateResults[i].MaxVal*100)
					}
				case 'p', 'P':
					process = windows.GetForegroundWindow()
					log.Infof("process: %#X", process)
				case 'r', 'R':
					screenshooter.FramesElapsed = 0
					log.Info("screenshooter.FramesElapsed reset")
				}
			}

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
	err := screenshooter.GetImage(screenImage)
	if err != nil {
		return err
	}
	return imageToMat(screenImage, display)
}

const method = gocv.TmCcoeffNormed

var lastSuccessfulTempl int

func doMatchWeapon(captureRoi gocv.Mat) (templateIndex, templateMatched int, found bool) {
	for j := range templates {
		i := j + lastSuccessfulTempl // 从上次成功的模板开始往下匹配
		i %= len(templates)
		templateIndex = i
		tmpl := templates[i]

		tStart := time.Now()
		panicIf(gocv.MatchTemplate(captureRoi, tmpl.Mat, &templateResults[i].Mat, method, tmpl.Mask))
		templateResults[i].Cost = time.Since(tStart)

		templateResults[i].MinMaxLoc()
		templateMatched++

		if templateResults[i].MaxVal >= matchThreshold {
			lastSuccessfulTempl = i
			found = true
			break // 跳过剩余匹配
		}
	}
	return
}

const fpsMaxHistoryLen = 30

var fpsCounter = NewFpsCounter(time.Second / 2)

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

	colorPos := colorGreen
	colorNeg := colorCyan
	min, max := templateResults.MinMax()
	var tmplPosMat, tmplNegMat Template
	var tmplPosResult, tmplNegResult TemplateResult
	if !found {
		// 黄框显示最高匹配的模板
		colorPos = colorYellow
		tmplIndex = max
	}
	tmplPosMat, tmplPosResult = templates[tmplIndex], templateResults[tmplIndex]
	tmplNegMat, tmplNegResult = templates[min], templateResults[min]
	if tmplPosResult.MaxVal > 0 {
		resultPosX := tmplPosResult.MaxLoc.X + roiRect.Min.X
		resultPosY := tmplPosResult.MaxLoc.Y + roiRect.Min.Y
		rectPos := image.Rect(
			resultPosX,
			resultPosY,
			resultPosX+tmplPosMat.Width,
			resultPosY+tmplPosMat.Height,
		)
		gocv.Rectangle(display,
			rectPos,
			colorPos, borderThickness,
		)

		textPosX := roiRect.Max.X + fontSpacingX
		textPosY := roiRect.Min.Y + fontHeight
		gocv.PutText(display,
			tmplPosMat.Name,
			image.Pt(textPosX, textPosY),
			gocv.FontHersheyDuplex, fontSize,
			colorPos, fontThickness,
		)
		textPosY += fontHeight + fontSpacingY
		gocv.PutText(display,
			fmt.Sprintf("%.2f%%", tmplPosResult.MaxVal*100),
			image.Pt(textPosX, textPosY),
			gocv.FontHersheyDuplex, fontSize,
			colorPos, fontThickness,
		)

		if drawNeg {
			resultNegX := tmplNegResult.MaxLoc.X + roiRect.Min.X
			resultNegY := tmplNegResult.MaxLoc.Y + roiRect.Min.Y
			rectNeg := image.Rect(
				resultNegX,
				resultNegY,
				resultNegX+tmplNegMat.Width,
				resultNegY+tmplNegMat.Height,
			)
			gocv.Rectangle(display,
				rectNeg,
				colorNeg, borderThickness,
			)
			textNegX := textPosX
			textNegY := textPosY + 2*(fontHeight+fontSpacingY)
			gocv.PutText(display,
				tmplNegMat.Name,
				image.Pt(textNegX, textNegY),
				gocv.FontHersheyDuplex, fontSize,
				colorNeg, fontThickness,
			)
			textNegY += fontHeight + fontSpacingY
			gocv.PutText(display,
				fmt.Sprintf("%.2f%%", tmplNegResult.MinVal*100),
				image.Pt(textNegX, textNegY),
				gocv.FontHersheyDuplex, fontSize,
				colorNeg, fontThickness,
			)
		}

		// 匹配的模板本身
		tmplPosRect := image.Rect(
			roiRect.Min.X, // 与ROI左对齐
			roiRect.Max.Y+borderThickness,
			roiRect.Min.X+tmplPosMat.Width,
			roiRect.Max.Y+borderThickness+tmplPosMat.Height,
		)
		roiPos := display.Region(tmplPosRect)
		defer roiPos.Close()
		if tmplPosMat.Mat.Channels() == 3 && roiPos.Channels() == 3 {
			tmplPosMat.Mat.CopyTo(&roiPos)
		} else if tmplPosMat.Mat.Channels() == 1 && roiPos.Channels() == 3 {
			colorTemplate := gocv.NewMat()
			defer colorTemplate.Close()
			gocv.CvtColor(tmplPosMat.Mat, &colorTemplate, gocv.ColorGrayToBGR)
			colorTemplate.CopyTo(&roiPos)
		}

		if drawNeg {
			tmplNegRect := image.Rect(
				roiRect.Min.X,
				tmplPosRect.Max.Y,
				roiRect.Min.X+tmplNegMat.Width,
				tmplPosRect.Max.Y+tmplNegMat.Height,
			)
			roiNeg := display.Region(tmplNegRect)
			defer roiNeg.Close()
			if tmplNegMat.Mat.Channels() == 3 && roiNeg.Channels() == 3 {
				tmplNegMat.Mat.CopyTo(&roiNeg)
			} else if tmplNegMat.Mat.Channels() == 1 && roiNeg.Channels() == 3 {
				colorTemplate := gocv.NewMat()
				defer colorTemplate.Close()
				gocv.CvtColor(tmplNegMat.Mat, &colorTemplate, gocv.ColorGrayToBGR)
				colorTemplate.CopyTo(&roiNeg)
			}
		}
	}

	gocv.PutText(display,
		fmt.Sprintf(
			"| FPS: %.2f | SSC: %dms | TMC: %dms/%d=%dms | ISC: %dms | %d |",
			fpsCounter.Count(),
			screenshotCost/time.Millisecond,
			templatesMatchingCost/time.Millisecond, tmplMatched,
			templatesMatchingCost/time.Duration(tmplMatched)/time.Millisecond,
			imShowCost/time.Millisecond,
			screenshooter.FramesElapsed,
		),
		image.Pt(10, 60),
		gocv.FontHersheyDuplex, 2,
		colorCoral, 4,
	)
}
