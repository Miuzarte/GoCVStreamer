package libyuv

import "image"

const kFilterBilinear = 2

// ResizeRGBA 使用 libyuv ARGBScale 做双线性缩放
func ResizeRGBA(src *image.RGBA, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	ResizeRGBAInto(dst, src, w, h)
	return dst
}

// ResizeRGBAInto 使用 libyuv ARGBScale 做双线性缩放, dst 保持 Go 的 R,G,B,A 字节序
//
// ARGBScale 对四个字节通道使用同一组滤波权重, 因此与 ARGB<->ABGR 换序可交换
// (见 resize_test.go 的 TestScaleCommutesWithChannelSwap): 直接把 Go 的 R,G,B,A
// 数据当 ARGB 缩放, 结果仍是 R,G,B,A。所以既不需要预先换序, 也不会再原地改写 src
func ResizeRGBAInto(dst *image.RGBA, src *image.RGBA, w, h int) {
	argbScale(
		&src.Pix[0], int32(src.Stride), int32(src.Bounds().Dx()), int32(src.Bounds().Dy()),
		&dst.Pix[0], int32(dst.Stride), int32(w), int32(h),
		kFilterBilinear,
	)
}

// ResizeBGRAInto 同 ResizeRGBAInto, 但结果换成 B,G,R,A 字节序 —— 也就是 OpenCV 的
// CV_8UC4/BGRA, 供 gocv.IMEncode 直接编码, 不需要再额外换通道。
// 换序只作用在缩小后的目标上 (InputSize 见方), 不碰源
func ResizeBGRAInto(dst *image.RGBA, src *image.RGBA, w, h int) {
	ResizeRGBAInto(dst, src, w, h)
	argbToABGR(&dst.Pix[0], int32(dst.Stride), &dst.Pix[0], int32(dst.Stride), int32(w), int32(h))
}
