package main

import (
	"fmt"
	"image"
	"io/fs"
	"math"
	"path/filepath"
	"strings"
	"time"

	"gocv.io/x/gocv"
)

type TemplateMode int

func (tm TemplateMode) String() string {
	switch tm {
	case TEMPLATE_MODE_FA:
		return "FullAuto"
	case TEMPLATE_MODE_SA:
		return "SemiAuto"
	default:
		return fmt.Sprintf("unexpected value: %d", tm)
	}
}

const (
	TEMPLATE_MODE_FA = iota + 1
	TEMPLATE_MODE_SA
)

type Template struct {
	Name      string
	Mode      TemplateMode // FA | SA
	SpeedAcog string       // generally with acog
	Speed1x   string

	gocv.Mat
	Width, Height int

	Mask gocv.Mat
}

func (t *Template) IMReadFrom(path string, createMask bool) error {
	t.Close()
	base := filepath.Base(path) // "{FA_13_13}9x19VSN.png"
	dotI := strings.LastIndexByte(base, '.')
	if dotI < 0 {
		return fmt.Errorf("invalid file name: %s", base)
	}
	filename := base[:dotI] // "{FA_13_13}9x19VSN"
	bracesL, bracesR := strings.IndexByte(filename, '{'), strings.IndexByte(filename, '}')
	if bracesL < 0 || bracesR < 0 {
		return fmt.Errorf("invalid file params: %s", base)
	}
	params := strings.Split(filename[bracesL+1:bracesR], "_") // ["FA", "13", "13"]
	if len(params) != 3 {
		return fmt.Errorf("invalid file params: %s", params)
	}
	switch params[0] {
	case "FA":
		t.Mode = TEMPLATE_MODE_FA
	case "SA":
		t.Mode = TEMPLATE_MODE_SA
	default:
		return fmt.Errorf("invalid file params mode: %s", params[0])
	}

	t.Name = filename[bracesR+1:]
	t.SpeedAcog = params[1]
	t.Speed1x = params[2]

	t.Mask = gocv.NewMat()
	if !createMask {
		t.Mat = gocv.IMRead(path, gocv.IMReadColor)
	} else {
		img := gocv.IMRead(path, gocv.IMReadUnchanged)
		t.Mat = gocv.NewMat()
		switch img.Channels() {
		case 4:
			channels := gocv.Split(img)
			bgrChannels := []gocv.Mat{channels[0], channels[1], channels[2]}
			gocv.Merge(bgrChannels, &t.Mat)
			gocv.Threshold(channels[3], &t.Mask, 64, 255, gocv.ThresholdBinary)
			if gocv.CountNonZero(t.Mask) == 0 {
				log.Warnf("unexpected zero mask: %s", t.Name)
			}
			for i := range channels {
				channels[i].Close()
			}

		case 3:
			img.ConvertTo(&t.Mat, gocv.MatTypeCV8UC3)
			log.Warnf("unexpected channels num 3 of image: %s", t.Name)
		case 1:
			img.ConvertTo(&t.Mat, gocv.MatTypeCV8UC1)
			log.Warnf("unexpected channels num 1 of image: %s", t.Name)

		default:
			return fmt.Errorf("unsupported number of channels: %d", img.Channels())
		}
	}

	t.Width, t.Height = t.Mat.Cols(), t.Mat.Rows()

	return nil
}

func (t *Template) Close() error {
	var err1, err2 error

	if !t.Mat.Closed() && !t.Mat.Empty() {
		err1 = t.Mat.Close()
	}

	if !t.Mask.Closed() && !t.Mask.Empty() {
		err2 = t.Mask.Close()
	}

	if err1 != nil {
		return err1
	}
	if err2 != nil {
		return err2
	}
	return nil
}

type Templates []Template

func (ts *Templates) IMReadDir(dir string, depth int, createMask bool) error {
	const suffix = ".png"
	depth = max(depth, 1)
	paths, err := walkDir(dir, depth, suffix, "__")
	if err != nil {
		return fmt.Errorf("failed to walk %s: %w", suffix, err)
	}

	*ts = make(Templates, len(paths))
	for i, path := range paths {
		err = (*ts)[i].IMReadFrom(path, createMask)
		if err != nil {
			return fmt.Errorf("[%d] %w", i, err)
		}
	}

	return nil
}

func (ts *Templates) CloseAll() error {
	for _, tmpl := range *ts {
		err := tmpl.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

type TemplateResult struct {
	Cost time.Duration
	gocv.Mat
	MinVal, MaxVal float32
	MinLoc, MaxLoc image.Point
}

func (tr *TemplateResult) MinMaxLoc() {
	tr.MinVal, tr.MaxVal, tr.MinLoc, tr.MaxLoc = gocv.MinMaxLoc(tr.Mat)
	minVal := float64(tr.MinVal)
	if math.IsNaN(minVal) || math.IsInf(minVal, 0) {
		tr.MinVal = 1.0
	}
	maxVal := float64(tr.MaxVal)
	if math.IsNaN(maxVal) || math.IsInf(maxVal, 0) {
		tr.MaxVal = -1.0
	}
}

type TemplateResults []TemplateResult

func (trs *TemplateResults) MinMax() (min, max int) {
	var maxMinVal, maxMaxVal float32
	for i, tmpl := range *trs {
		if tmpl.MinVal > maxMinVal {
			min = i
		}
		if tmpl.MaxVal > maxMaxVal {
			max = i
		}
	}
	return
}

func walkDir(dir string, depth int, suffix string, withoutPrefix string) (paths []string, err error) {
	dir = filepath.Clean(dir)
	if !strings.HasPrefix(suffix, ".") {
		suffix = "." + suffix
	}

	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		relDepth := 0
		if relPath != "." {
			relDepth = strings.Count(relPath, string(filepath.Separator)) + 1
		}
		if relDepth > depth && d.IsDir() {
			return filepath.SkipDir
		}

		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), suffix) && !strings.HasPrefix(filepath.Base(path), withoutPrefix) {
			if relDepth <= depth {
				paths = append(paths, path)
			}
		}

		return nil
	})

	return
}
