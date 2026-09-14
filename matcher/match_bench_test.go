package matcher

import (
	"image"
	"sync"
	"testing"
	"time"

	"github.com/Miuzarte/GoCVStreamer/weapon"
	"github.com/Miuzarte/GoCVStreamer/weapons"
	"gocv.io/x/gocv"
)

// TestMatchWeaponCost 单独测量一轮 matchWeapon 的成本, 用于回答
// "88x104 的 ROI, 115 个模板, 为什么 match_cost_ms 有 20ms"
func TestMatchWeaponCost(t *testing.T) {
	var ws weapons.Weapons
	if err := ws.ReadFrom("../templates", 1, ".png", "__", false, gocv.IMReadGrayScale); err != nil {
		t.Skipf("load templates: %v", err)
	}
	defer ws.Close()
	if len(ws) == 0 {
		t.Skip("no templates")
	}

	t.Logf("templates = %d", len(ws))
	for i, w := range ws {
		if i >= 3 {
			break
		}
		t.Logf("  [%d] %s  mat=%dx%d", i, w.String(), w.Template.Width, w.Template.Height)
	}

	var mu sync.RWMutex
	e := &Engine{cfg: Config{Weapons: ws, WeaponsMu: &mu}}

	// 与真实 ROI 同尺寸的灰度图
	const roiW, roiH = 88, 104
	roi := gocv.NewMatWithSize(roiH, roiW, gocv.MatTypeCV8UC1)
	defer roi.Close()
	pix, err := roi.DataPtrUint8()
	if err != nil {
		t.Fatal(err)
	}
	for i := range pix {
		pix[i] = uint8(i * 7)
	}

	// 预热
	for range 3 {
		e.matchWeapon(roi, weapon.SLOT_UNDEFINED)
	}

	// 单次 MatchTemplate 的成本 (拿第 0 个模板)
	single := gocv.NewMat()
	defer single.Close()
	tmpl := ws[0].Template
	t0 := time.Now()
	const reps = 200
	for range reps {
		if err := tmpl.Match(roi, gocv.TmCcoeffNormed); err != nil {
			t.Fatal(err)
		}
	}
	singleCost := time.Since(t0) / reps
	t.Logf("单次 Template.Match (%dx%d 模板 vs %dx%d ROI) = %v, 结果 %dx%d",
		tmpl.Width, tmpl.Height, roiW, roiH, singleCost, tmpl.MaxLoc.X, tmpl.MaxLoc.Y)

	// 单次 MinMaxLoc 的成本
	mm := gocv.NewMatWithSize(8, 8, gocv.MatTypeCV32FC1)
	defer mm.Close()
	t0 = time.Now()
	for range reps {
		gocv.MinMaxLoc(mm)
	}
	t.Logf("单次 MinMaxLoc = %v", time.Since(t0)/reps)

	// 一整轮 (无匹配时跑满全部模板)
	t0 = time.Now()
	const rounds = 20
	for range rounds {
		e.matchWeapon(roi, weapon.SLOT_UNDEFINED)
	}
	total := time.Since(t0) / rounds
	t.Logf("一轮 matchWeapon (全模板扫完) = %v, 其中单模板占比 %v",
		total, singleCost*time.Duration(len(ws)))

	// 注: 想单独量化 "AVX2 dispatch 对模板匹配的贡献", 需要 cv::setUseOptimized(false) 做对照,
	// 但 gocv v0.43.0 没暴露这个 API (只有 Set/GetNumThreads)。为这点收益给测试文件加 cgo 不值,
	// 所以这里只记录绝对耗时; 版本间比较见 .bench/CPU-归因报告.md 第 8.1.1 节
	_ = image.Point{}
}
