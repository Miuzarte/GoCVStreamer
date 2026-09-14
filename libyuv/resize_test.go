package libyuv

import (
	"fmt"
	"image"
	"image/draw"
	"slices"
	"testing"
	"time"

	"github.com/kbinani/screenshot"
)

func TestResizeRgba(t *testing.T) {
	n := screenshot.NumActiveDisplays()
	if n == 0 {
		t.Skip("no active display")
	}

	var biggestBounds image.Rectangle
	for i := range n {
		b := screenshot.GetDisplayBounds(i)
		a := b.Dx() * b.Dy()
		if a > biggestBounds.Dx()*biggestBounds.Dy() {
			biggestBounds = b
		}
	}

	img, err := screenshot.CaptureRect(biggestBounds)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	srcW := img.Bounds().Dx()
	srcH := img.Bounds().Dy()
	cropSize := 1280
	if srcW < cropSize || srcH < cropSize {
		t.Skipf("display too small: %dx%d", srcW, srcH)
	}

	ox := (srcW - cropSize) / 2
	oy := (srcH - cropSize) / 2

	cropImg := image.NewRGBA(image.Rect(0, 0, cropSize, cropSize))
	draw.Draw(cropImg, cropImg.Bounds(), img, image.Pt(ox, oy), draw.Src)

	fmt.Printf("capture: %dx%d, crop: %dx%d (+%d,+%d)\n", srcW, srcH, cropSize, cropSize, ox, oy)

	const rounds = 10
	durations := make([]time.Duration, rounds)

	for i := range rounds {
		src := image.NewRGBA(image.Rect(0, 0, cropSize, cropSize))
		copy(src.Pix, cropImg.Pix)

		t0 := time.Now()
		dst := ResizeRGBA(src, 640, 640)
		d := time.Since(t0)
		durations[i] = d

		_ = dst
	}

	slices.Sort(durations)

	var total time.Duration
	for _, d := range durations {
		total += d
	}
	avg := total / rounds

	fmt.Printf("rounds=%d  min=%v  max=%v  avg=%v\n", rounds, durations[0], durations[rounds-1], avg)
}

// fillRandom 用确定性伪随机填充, 保证四个字节通道互不相同 (足以暴露通道顺序问题)
func fillRandom(img *image.RGBA, seed uint32) {
	x := seed
	for i := range img.Pix {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		img.Pix[i] = byte(x)
	}
}

// refResizeOld 是改动前的算法: 先把源原地换成 libyuv 的 ARGB 字节序, 缩放, 再把目标换回来
func refResizeOld(dst, src *image.RGBA, w, h int) {
	sw, sh := int32(src.Bounds().Dx()), int32(src.Bounds().Dy())
	ss := int32(src.Stride)
	abgrToARGB(&src.Pix[0], ss, &src.Pix[0], ss, sw, sh)
	argbScale(&src.Pix[0], ss, sw, sh, &dst.Pix[0], int32(dst.Stride), int32(w), int32(h), kFilterBilinear)
	argbToABGR(&dst.Pix[0], int32(dst.Stride), &dst.Pix[0], int32(dst.Stride), int32(w), int32(h))
}

// TestScaleCommutesWithChannelSwap 验证 ARGBScale 与 ARGB<->ABGR 换序可交换:
// 直接对 Go 的 R,G,B,A 数据做 scale 就能得到与旧算法完全相同的输出,
// 从而可以去掉两次全图换序, 并让缩放不再原地改写源数据 (共享帧缓冲的前提)
func TestScaleCommutesWithChannelSwap(t *testing.T) {
	const (
		sw, sh = 1280, 1280
		dw, dh = 640, 640
	)

	src := image.NewRGBA(image.Rect(0, 0, sw, sh))
	fillRandom(src, 0x9e3779b9)

	// 参考: 旧算法 (会原地改写 oldSrc)
	oldSrc := image.NewRGBA(src.Bounds())
	copy(oldSrc.Pix, src.Pix)
	oldDst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	refResizeOld(oldDst, oldSrc, dw, dh)

	// 新算法: 只做一次 ARGBScale, 源是 R,G,B,A 直接当 ARGB 用
	newSrc := image.NewRGBA(src.Bounds())
	copy(newSrc.Pix, src.Pix)
	newDst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	argbScale(
		&newSrc.Pix[0], int32(newSrc.Stride), sw, sh,
		&newDst.Pix[0], int32(newDst.Stride), dw, dh,
		kFilterBilinear,
	)

	if !slices.Equal(oldDst.Pix, newDst.Pix) {
		diff := 0
		var maxDiff byte
		for i := range oldDst.Pix {
			if oldDst.Pix[i] != newDst.Pix[i] {
				diff++
				d := oldDst.Pix[i] - newDst.Pix[i]
				if oldDst.Pix[i] < newDst.Pix[i] {
					d = newDst.Pix[i] - oldDst.Pix[i]
				}
				if d > maxDiff {
					maxDiff = d
				}
			}
		}
		t.Fatalf("scale 与换序不可交换: %d/%d 字节不同, 最大差 %d", diff, len(oldDst.Pix), maxDiff)
	}

	// 顺带确认旧算法确实会改写源 (新算法不会)
	if slices.Equal(oldSrc.Pix, src.Pix) {
		t.Log("注意: 旧算法这次没有改写源, 与注释描述不符")
	}
	if !slices.Equal(newSrc.Pix, src.Pix) {
		t.Fatal("新算法不应改写源数据")
	}
}

// TestResizeBGRAIntoLayout 验证 ResizeBGRAInto 输出的是 B,G,R,A 字节序
func TestResizeBGRAIntoLayout(t *testing.T) {
	const sw, sh, dw, dh = 64, 64, 16, 16

	base := image.NewRGBA(image.Rect(0, 0, sw, sh))
	fillRandom(base, 0x1234567)

	srcForOld := image.NewRGBA(base.Bounds())
	copy(srcForOld.Pix, base.Pix)
	want := image.NewRGBA(image.Rect(0, 0, dw, dh))
	refResizeOld(want, srcForOld, dw, dh)

	src := image.NewRGBA(base.Bounds())
	copy(src.Pix, base.Pix)
	got := image.NewRGBA(image.Rect(0, 0, dw, dh))
	ResizeBGRAInto(got, src, dw, dh)

	for i := 0; i < len(got.Pix); i += 4 {
		want.Pix[i+0], want.Pix[i+2] = want.Pix[i+2], want.Pix[i+0]
	}
	if !slices.Equal(want.Pix, got.Pix) {
		t.Fatal("ResizeBGRAInto 输出不是旧算法结果按 B<->R 交换后的字节序")
	}
}
