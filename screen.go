package main

import (
	"bufio"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/Miuzarte/GoCVStreamer/capture"
	"github.com/kbinani/screenshot"
	"gocv.io/x/gocv"
)

var (
	capturer    *capture.DxgiDesktopDuplicator
	screenImage *image.RGBA
)

func selectDisplay() {
	numDisplays := screenshot.NumActiveDisplays()
	log.Info().
		Int("activeDisplays", numDisplays).
		Msg("active displays detected")
	displayBoundaries := make([]image.Rectangle, numDisplays)
	for i := range numDisplays {
		displayBoundaries[i] = screenshot.GetDisplayBounds(i)
	}

	displayIndex := 0
	if numDisplays > 1 {
		log.Info().Msg("multi displays detected")
		for i := range numDisplays {
			size := displayBoundaries[i].Size()
			fmt.Fprintf(os.Stdout, "[%d] %dx%d (X:%d, Y:%d)\n", i, size.X, size.Y, displayBoundaries[i].Min.X, displayBoundaries[i].Min.Y)
		}

		reader := bufio.NewReader(os.Stdin)
		for {
			fmt.Fprintf(os.Stdout, "input index in range [0,%d]: ", numDisplays-1)
			input, err := reader.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				log.Panic().Err(err).Msg("failed to read os.Stdin")
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
				log.Info().
					Int("displayIndex", displayIndex).
					Msg("auto selected display")
			}

			break
		}
	}

	displayBounds := displayBoundaries[displayIndex]
	log.Info().
		Int("displayIndex", displayIndex).
		Int("width", displayBounds.Dx()).
		Int("height", displayBounds.Dy()).
		Msg("using display")

	var err error
	capturer, err = capture.New(displayIndex)
	panicIf(err)
	if !capturer.Bounds().Eq(displayBounds) {
		log.Warn().
			Any("capturerBounds", capturer.Bounds()).
			Any("displayBounds", displayBounds).
			Msg("capturer bounds mismatch")
	}
	screenImage = image.NewRGBA(capturer.Bounds())
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
			log.Warn().Msg("unexpected image color model, conversion performance may be affected")
		})
		if MATCHING_MODE == gocv.IMReadGrayScale {
			data := make([]byte, 0, x*y)
			for j := bounds.Min.Y; j < bounds.Max.Y; j++ {
				for i := bounds.Min.X; i < bounds.Max.X; i++ {
					r, g, b, _ := img.At(i, j).RGBA()
					gray := byte((19595*uint32(r) + 38470*uint32(g) + 7471*uint32(b)) >> 16)
					data = append(data, gray)
				}
			}
			src, err = gocv.NewMatFromBytes(y, x, gocv.MatTypeCV8UC1, data)
			if err != nil {
				return err
			}
			defer src.Close()
		} else {
			data := make([]byte, 0, x*y*3)
			for j := bounds.Min.Y; j < bounds.Max.Y; j++ {
				for i := bounds.Min.X; i < bounds.Max.X; i++ {
					r, g, b, _ := img.At(i, j).RGBA()
					data = append(data, byte(b>>8), byte(g>>8), byte(r>>8))
				}
			}
			src, err = gocv.NewMatFromBytes(y, x, gocv.MatTypeCV8UC3, data)
			if err != nil {
				return err
			}
			defer src.Close()
		}
	}

	if MATCHING_MODE == gocv.IMReadGrayScale {
		return gocv.CvtColor(src, dst, gocv.ColorRGBAToGray)
	}
	return gocv.CvtColor(src, dst, gocv.ColorRGBAToBGR)
}
