package wgc

import (
	"image"
	"os"
	"testing"

	"github.com/Miuzarte/GoCVStreamer/capturer"
	"github.com/kirides/go-d3d/outputduplication"
	"golang.org/x/sys/windows"
)

func TestSupported(t *testing.T) {
	if !Supported() {
		// 无法区分"系统不支持"与"DLL 缺失"，两种情况下都跳过断言，仅保证不 panic。
		t.Log("WGC not supported or wgc_helper.dll missing")
		return
	}
	t.Log("WGC supported")
}

// TestManualCaptureFrames 手动验证：设置 WGC_TEST_CAPTURE=1 后运行，
// 从显示器 0 抓取若干帧并校验尺寸与数据。
func TestManualCaptureFrames(t *testing.T) {
	if os.Getenv("WGC_TEST_CAPTURE") == "" {
		t.Skip("set WGC_TEST_CAPTURE=1 to run manual capture test")
	}
	if !Supported() {
		t.Skip("WGC unsupported or wgc_helper.dll missing")
	}
	var sessionID uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &sessionID); err == nil && sessionID == 0 {
		t.Skip("running in session 0 (service session); WGC requires an interactive desktop session")
	}

	src, err := NewDisplaySource(0)
	if err != nil {
		t.Fatalf("NewDisplaySource: %v", err)
	}
	defer src.Close()

	bounds := src.Bounds()
	t.Logf("initial bounds: %v", bounds)
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		t.Fatalf("invalid bounds: %v", bounds)
	}

	img := image.NewRGBA(bounds)
	got := 0
	nonZero := false
	for i := 0; i < 10; i++ {
		err := src.GetImageTimeout(img, 2000)
		if err == outputduplication.ErrNoImageYet {
			continue
		}
		if err == capturer.ErrSizeChanged {
			img = image.NewRGBA(src.Bounds())
			continue
		}
		if err != nil {
			t.Fatalf("GetImageTimeout: %v", err)
		}
		got++
		for _, v := range img.Pix {
			if v != 0 {
				nonZero = true
				break
			}
		}
		if nonZero {
			break
		}
	}
	if got == 0 {
		t.Fatal("no frame captured within 10 attempts")
	}
	if !nonZero {
		t.Fatal("captured frames are all black")
	}
	t.Logf("captured %d frame(s), size=%dx%d", got, img.Bounds().Dx(), img.Bounds().Dy())
}
