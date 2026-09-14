package capturer

import (
	"image"
	"image/color"
	"testing"

	"gocv.io/x/gocv"
)

// fakeSource 是最小的 Source 实现, 只为在没有真实采集源时构造 Server
type fakeSource struct {
	bounds image.Rectangle
}

func (f *fakeSource) Bounds() image.Rectangle                        { return f.bounds }
func (f *fakeSource) GetImage(*image.RGBA) error                     { return nil }
func (f *fakeSource) GetImageTimeout(*image.RGBA, uint) error        { return nil }
func (f *fakeSource) ProvideMat(*gocv.Mat) bool                      { return false }
func (f *fakeSource) FramesElapsed() int                             { return 0 }
func (f *fakeSource) ResetFramesElapsed()                            {}
func (f *fakeSource) Close() error                                   { return nil }

func newTestServer(t *testing.T, bounds, roi image.Rectangle) *Server {
	t.Helper()
	s := NewServer(&fakeSource{bounds: bounds}, Config{MinFps: 1, MatchRoi: roi}, gocv.IMReadGrayScale, nil)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// fillGradient 用确定性梯度填充图像, 保证 RGB 三通道互不相同 (能暴露通道顺序错误)
func fillGradient(img *image.RGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(x*3 + y),
				G: uint8(y*5 + x),
				B: uint8(x*7 - y*2),
				A: 255,
			})
		}
	}
}

// TestImageToMatRoiOnly 验证灰度转换只做 MatchRoi, 且结果与"整帧转换后取同一区域"逐像素一致
func TestImageToMatRoiOnly(t *testing.T) {
	bounds := image.Rect(0, 0, 64, 48)
	roi := image.Rect(9, 13, 9+11, 13+13)

	s := newTestServer(t, bounds, roi)
	if s.matchRoi != roi {
		t.Fatalf("matchRoi = %v, want %v", s.matchRoi, roi)
	}

	img := image.NewRGBA(bounds)
	fillGradient(img)

	got := gocv.NewMat()
	defer got.Close()
	if err := s.imageToMat(img, &got); err != nil {
		t.Fatal(err)
	}
	if got.Cols() != roi.Dx() || got.Rows() != roi.Dy() {
		t.Fatalf("mat = %dx%d, want %dx%d", got.Cols(), got.Rows(), roi.Dx(), roi.Dy())
	}
	if got.Type() != gocv.MatTypeCV8UC1 {
		t.Fatalf("mat type = %v, want CV_8UC1", got.Type())
	}

	// 参考实现: 整帧 RGBA -> 灰度, 再取 ROI
	whole := gocv.NewMatWithSize(bounds.Dy(), bounds.Dx(), gocv.MatTypeCV8UC4)
	defer whole.Close()
	data, err := whole.DataPtrUint8()
	if err != nil {
		t.Fatal(err)
	}
	copy(data, img.Pix)

	ref := gocv.NewMat()
	defer ref.Close()
	if err := gocv.CvtColor(whole, &ref, gocv.ColorRGBAToGray); err != nil {
		t.Fatal(err)
	}
	refRoi := ref.Region(roi)
	defer refRoi.Close()

	diff := gocv.NewMat()
	defer diff.Close()
	gocv.AbsDiff(got, refRoi, &diff)
	_, maxVal, _, _ := gocv.MinMaxLoc(diff)
	if maxVal != 0 {
		t.Fatalf("ROI 灰度与整帧参考不一致, maxdiff = %v", maxVal)
	}
}

// TestImageToMatDefaultRoi 未指定 MatchRoi 时应退化为整帧
func TestImageToMatDefaultRoi(t *testing.T) {
	bounds := image.Rect(0, 0, 32, 24)
	s := newTestServer(t, bounds, image.Rectangle{})
	if s.matchRoi != bounds {
		t.Fatalf("matchRoi = %v, want bounds %v", s.matchRoi, bounds)
	}

	img := image.NewRGBA(bounds)
	fillGradient(img)

	got := gocv.NewMat()
	defer got.Close()
	if err := s.imageToMat(img, &got); err != nil {
		t.Fatal(err)
	}
	if got.Cols() != bounds.Dx() || got.Rows() != bounds.Dy() {
		t.Fatalf("mat = %dx%d, want %dx%d", got.Cols(), got.Rows(), bounds.Dx(), bounds.Dy())
	}
}

// TestSetMatchRoi 验证 ROI 变化后缓冲会重建并继续正确
func TestSetMatchRoi(t *testing.T) {
	bounds := image.Rect(0, 0, 64, 48)
	s := newTestServer(t, bounds, image.Rect(0, 0, 8, 8))

	newRoi := image.Rect(20, 10, 20+16, 10+12)
	s.SetMatchRoi(newRoi)
	if s.matchRoi != newRoi {
		t.Fatalf("matchRoi = %v, want %v", s.matchRoi, newRoi)
	}

	img := image.NewRGBA(bounds)
	fillGradient(img)

	got := gocv.NewMat()
	defer got.Close()
	if err := s.imageToMat(img, &got); err != nil {
		t.Fatal(err)
	}
	if got.Cols() != newRoi.Dx() || got.Rows() != newRoi.Dy() {
		t.Fatalf("mat = %dx%d, want %dx%d", got.Cols(), got.Rows(), newRoi.Dx(), newRoi.Dy())
	}
}

// TestClampRoi 越界 / 空值都要退化成安全的区域
func TestClampRoi(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 80)

	if got := clampRoi(image.Rectangle{}, bounds); got != bounds {
		t.Errorf("empty roi -> %v, want %v", got, bounds)
	}
	if got := clampRoi(image.Rect(200, 200, 220, 220), bounds); got != bounds {
		t.Errorf("fully out of bounds roi -> %v, want %v", got, bounds)
	}
	if got := clampRoi(image.Rect(-10, -10, 20, 20), bounds); got != image.Rect(0, 0, 20, 20) {
		t.Errorf("partially out of bounds roi -> %v, want %v", got, image.Rect(0, 0, 20, 20))
	}
}
