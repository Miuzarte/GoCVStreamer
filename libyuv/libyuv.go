package libyuv

import (
	"syscall"

	"github.com/ebitengine/purego"
)

var (
	dllHandle       uintptr
	argbScale       func(src *byte, srcStride int32, srcW int32, srcH int32, dst *byte, dstStride int32, dstW int32, dstH int32, filter int32) int32
	abgrToARGB      func(src *byte, srcStride int32, dst *byte, dstStride int32, width int32, height int32) int32
	argbToABGR      func(src *byte, srcStride int32, dst *byte, dstStride int32, width int32, height int32) int32
)

func init() {
	h, err := syscall.LoadLibrary(`B:\Git\libyuv\build\libyuv.dll`)
	if err != nil {
		panic(err)
	}
	dllHandle = uintptr(h)
	purego.RegisterLibFunc(&argbScale, dllHandle, "ARGBScale")
	purego.RegisterLibFunc(&abgrToARGB, dllHandle, "ABGRToARGB")
	purego.RegisterLibFunc(&argbToABGR, dllHandle, "ARGBToABGR")
}
