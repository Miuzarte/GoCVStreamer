package libyuv

import "image"

const kFilterBilinear = 2

// ResizeRGBA 使用 libyuv ARGBScale 做双线性缩放
//
// src 必须是已拷贝的独立图像, 函数会原地修改其 Pix（RGBA <-> ARGB 转换）
func ResizeRGBA(src *image.RGBA, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	ResizeRGBAInto(dst, src, w, h)
	return dst
}

func ResizeRGBAInto(dst *image.RGBA, src *image.RGBA, w, h int) {
	srcW := src.Bounds().Dx()
	srcH := src.Bounds().Dy()
	srcStride := int32(src.Stride)

	abgrToARGB(&src.Pix[0], srcStride, &src.Pix[0], srcStride, int32(srcW), int32(srcH))

	dstW := w
	dstH := h
	dstStride := int32(dst.Stride)

	argbScale(
		&src.Pix[0], srcStride, int32(srcW), int32(srcH),
		&dst.Pix[0], dstStride, int32(dstW), int32(dstH),
		kFilterBilinear,
	)

	argbToABGR(&dst.Pix[0], dstStride, &dst.Pix[0], dstStride, int32(dstW), int32(dstH))
}
