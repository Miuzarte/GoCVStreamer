// Command jpegbench 对比两条 JPEG 编码路径的真实成本:
//
//	1. Go 标准库 image/jpeg (sender 当前用法)
//	2. OpenCV (libopencv_imgcodecs 内置静态链接的 libjpeg-turbo) 的 imencode
//
// 几何与 streamer sender 一致: 读一张整屏帧 -> 中心裁剪 -> libyuv 缩放到 640x640 RGBA,
// 再分别编码。同时校验解码回来的像素, 防止 4 通道 (RGBA/BGRA) 顺序搞错导致颜色反了。
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"time"

	"github.com/Miuzarte/GoCVStreamer/libyuv"
	"gocv.io/x/gocv"
	"golang.org/x/sys/windows"
)

var (
	pngPath = flag.String("png", "", "整屏源帧 png (如 2560x1440)")
	quality = flag.Int("q", 80, "JPEG 质量 1-100")
	iters   = flag.Int("iter", 300, "每个变体的迭代次数")
	side    = flag.Int("side", 640, "缩放后的正方形边长")
	crop    = flag.Int("crop", 1280, "缩放前的中心裁剪边长")
)

// cpuSeconds 返回本进程累计 (kernel + user) CPU 秒数, 与外部 Get-Process 口径一致
func cpuSeconds() float64 {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &creation, &exit, &kernel, &user); err != nil {
		panic(err)
	}
	toSec := func(ft windows.Filetime) float64 {
		return float64(uint64(ft.HighDateTime)<<32|uint64(ft.LowDateTime)) / 1e7
	}
	return toSec(kernel) + toSec(user)
}

type result struct {
	name  string
	wall  time.Duration
	cpu   time.Duration
	bytes int
}

func (r result) String() string {
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) / float64(*iters) }
	return fmt.Sprintf("%-14s %8.3f ms/frame(wall) %8.3f ms/frame(cpu) %8d bytes  %7.1f fps(wall) %7.1f fps(cpu)",
		r.name, ms(r.wall), ms(r.cpu), r.bytes/int(max(*iters, 1)),
		float64(*iters)/r.wall.Seconds(), float64(*iters)/r.cpu.Seconds())
}

func main() {
	flag.Parse()
	if *pngPath == "" {
		fmt.Fprintln(os.Stderr, "-png is required")
		os.Exit(2)
	}

	f, err := os.Open(*pngPath)
	if err != nil {
		panic(err)
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		panic(err)
	}
	full := image.NewRGBA(src.Bounds())
	draw.Draw(full, full.Bounds(), src, src.Bounds().Min, draw.Src)

	// 与 sender 相同的几何: 中心 crop x crop, 再缩放到 side x side
	b := full.Bounds()
	off := image.Pt((b.Dx()-*crop)/2, (b.Dy()-*crop)/2)
	cropped := image.NewRGBA(image.Rect(0, 0, *crop, *crop))
	draw.Draw(cropped, cropped.Bounds(), full, off, draw.Src)
	rgba := image.NewRGBA(image.Rect(0, 0, *side, *side))
	libyuv.ResizeRGBAInto(rgba, cropped, *side, *side)

	// OpenCV 的通道约定是 BGRA/BGR, 这里预先准备好副本 (真实集成里 libyuv 可以直接产出 BGRA)
	bgra := make([]byte, len(rgba.Pix))
	bgr := make([]byte, *side**side*3)
	for i := 0; i < len(rgba.Pix); i += 4 {
		bgra[i+0] = rgba.Pix[i+2]
		bgra[i+1] = rgba.Pix[i+1]
		bgra[i+2] = rgba.Pix[i+0]
		bgra[i+3] = rgba.Pix[i+3]
		j := (i / 4) * 3
		bgr[j+0] = rgba.Pix[i+2]
		bgr[j+1] = rgba.Pix[i+1]
		bgr[j+2] = rgba.Pix[i+0]
	}

	fmt.Printf("jpegbench source=%s geometry=%dx%d center-crop %d -> resize %d quality=%d iters=%d\n",
		*pngPath, b.Dx(), b.Dy(), *crop, *side, *quality, *iters)

	params := []int{gocv.IMWriteJpegQuality, *quality}

	mat4 := gocv.NewMatWithSize(*side, *side, gocv.MatTypeCV8UC4)
	defer mat4.Close()
	mat3 := gocv.NewMatWithSize(*side, *side, gocv.MatTypeCV8UC3)
	defer mat3.Close()

	dst4, err := mat4.DataPtrUint8()
	if err != nil {
		panic(err)
	}
	dst3, err := mat3.DataPtrUint8()
	if err != nil {
		panic(err)
	}

	var results []result
	var samples = map[string][]byte{}

	// 变体 1: Go 标准库
	{
		var buf bytes.Buffer
		var total int
		// 预热
		for range 5 {
			buf.Reset()
			_ = jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: *quality})
		}
		w0, c0 := time.Now(), cpuSeconds()
		for range *iters {
			buf.Reset()
			if err := jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: *quality}); err != nil {
				panic(err)
			}
			total += buf.Len()
		}
		w1, c1 := time.Now(), cpuSeconds()
		samples["go-std"] = append([]byte(nil), buf.Bytes()...)
		results = append(results, result{"go-std", w1.Sub(w0), time.Duration((c1 - c0) * float64(time.Second)), total})
	}

	// 变体 2: OpenCV imencode, 4 通道 (BGRA)
	{
		var total int
		copy(dst4, bgra)
		var last []byte
		for range 5 {
			buf, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, mat4, params)
			if err != nil {
				panic(err)
			}
			buf.Close()
		}
		w0, c0 := time.Now(), cpuSeconds()
		for range *iters {
			copy(dst4, bgra)
			buf, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, mat4, params)
			if err != nil {
				panic(err)
			}
			total += buf.Len()
			last = append(last[:0], buf.GetBytes()...)
			buf.Close()
		}
		w1, c1 := time.Now(), cpuSeconds()
		samples["ocv-bgra4"] = append([]byte(nil), last...)
		results = append(results, result{"ocv-bgra4", w1.Sub(w0), time.Duration((c1 - c0) * float64(time.Second)), total})
	}

	// 变体 3: OpenCV imencode, 3 通道 (BGR)
	{
		var total int
		copy(dst3, bgr)
		var last []byte
		for range 5 {
			buf, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, mat3, params)
			if err != nil {
				panic(err)
			}
			buf.Close()
		}
		w0, c0 := time.Now(), cpuSeconds()
		for range *iters {
			copy(dst3, bgr)
			buf, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, mat3, params)
			if err != nil {
				panic(err)
			}
			total += buf.Len()
			last = append(last[:0], buf.GetBytes()...)
			buf.Close()
		}
		w1, c1 := time.Now(), cpuSeconds()
		samples["ocv-bgr3"] = append([]byte(nil), last...)
		results = append(results, result{"ocv-bgr3", w1.Sub(w0), time.Duration((c1 - c0) * float64(time.Second)), total})
	}

	// 变体 4: sender 改动后的真实路径 —— 每帧 draw.Draw 裁剪 + libyuv.ResizeBGRAInto 直接
	// 写进 Mat 的内存 + imencode。这条同时验证 ResizeBGRAInto 的字节序 (颜色不能反)
	{
		cropImg := image.NewRGBA(image.Rect(0, 0, *crop, *crop))
		dst := &image.RGBA{Pix: dst4, Stride: *side * 4, Rect: image.Rect(0, 0, *side, *side)}
		var total int
		var last []byte
		encode := func() *gocv.NativeByteBuffer {
			draw.Draw(cropImg, cropImg.Bounds(), full, off, draw.Src)
			libyuv.ResizeBGRAInto(dst, cropImg, *side, *side)
			buf, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, mat4, params)
			if err != nil {
				panic(err)
			}
			return buf
		}
		for range 5 {
			encode().Close()
		}
		w0, c0 := time.Now(), cpuSeconds()
		for range *iters {
			buf := encode()
			total += buf.Len()
			last = append(last[:0], buf.GetBytes()...)
			buf.Close()
		}
		w1, c1 := time.Now(), cpuSeconds()
		samples["prod-path"] = append([]byte(nil), last...)
		results = append(results, result{"prod-path", w1.Sub(w0), time.Duration((c1 - c0) * float64(time.Second)), total})
	}

	fmt.Println()
	for _, r := range results {
		fmt.Println(r)
	}

	fmt.Println()
	fmt.Println("--- 正确性校验 (解码回来与原图比) ---")	// 原图 RGB 均值
	var sr, sg, sb float64
	n := float64(*side * *side)
	for i := 0; i < len(rgba.Pix); i += 4 {
		sr += float64(rgba.Pix[i+0])
		sg += float64(rgba.Pix[i+1])
		sb += float64(rgba.Pix[i+2])
	}
	fmt.Printf("%-12s mean R/G/B = %6.2f %6.2f %6.2f | mean abs diff = 0.00\n", "source", sr/n, sg/n, sb/n)

	for _, name := range []string{"go-std", "ocv-bgra4", "ocv-bgr3", "prod-path"} {
		data := samples[name]
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			fmt.Printf("%-12s decode failed: %v\n", name, err)
			continue
		}
		dec := image.NewRGBA(image.Rect(0, 0, *side, *side))
		draw.Draw(dec, dec.Bounds(), img, img.Bounds().Min, draw.Src)

		var mr, mg, mb, diff float64
		for i := 0; i < len(rgba.Pix); i += 4 {
			dr := float64(int(dec.Pix[i+0]) - int(rgba.Pix[i+0]))
			dg := float64(int(dec.Pix[i+1]) - int(rgba.Pix[i+1]))
			db := float64(int(dec.Pix[i+2]) - int(rgba.Pix[i+2]))
			mr += float64(dec.Pix[i+0])
			mg += float64(dec.Pix[i+1])
			mb += float64(dec.Pix[i+2])
			diff += (abs(dr) + abs(dg) + abs(db)) / 3
		}
		fmt.Printf("%-12s mean R/G/B = %6.2f %6.2f %6.2f | mean abs diff = %5.2f\n",
			name, mr/n, mg/n, mb/n, diff/n)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
