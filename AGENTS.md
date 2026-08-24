# AGENTS.md

本文件为 AI/开发者提供 GoCVStreamer (streamer) 的快速上下文。仓库位于 `B:\Git\GoCVStreamer`, 模块名 `github.com/Miuzarte/GoCVStreamer`, Go 1.27.0, Windows-only。

## 项目是什么

streamer 是一个 Windows 游戏辅助采集/处理程序:
- 采集: DXGI / WGC / OBS 虚拟摄像头, 支持整屏与单窗口
- 匹配: OpenCV 模板匹配识别 R6S 当前武器 (templates/ 目录), 按文件名解析武器参数
- 检测: 本地 YOLO 人员检测 + 远程 (手机 NPU) 检测结果合并
- 辅助: 瞄准吸附 (assist), 压枪参数推送
- 推送: WebSocket 推流 (JPEG), HTTP metrics, 鼠标移动注入 (本地或经 mhub 远程)

## 仓库依赖链

```
logi-hidpp (HID++ 协议) ← mhub-logi (设备扩展) ← mhub-go (脚本运行时) ← GoCVStreamer
```

全部本地 replace (见 go.mod), 修改底层仓库后无需 go mod tidy 即可生效:
- `github.com/Miuzarte/logi-hidpp => B:\Git\logi-hidpp`
- `github.com/Miuzarte/mhub-logi => B:\Git\mhub-logi` (mhub-go 的 go.mod)
- `github.com/Miuzarte/mhub-go => B:\Git\mhub-go`
- `github.com/getcharzp/go-vision => B:\Git\go-vision`
- `github.com/getcharzp/onnxruntime_purego => B:\Git\onnxruntime_purego`

## 目录与包

根包 `main` (主程序):

| 文件 | 职责 |
|---|---|
| `main.go` | 入口, flag 定义, `cwg` 生命周期, 各循环 (r6sLoop/weaponAltLoop/cpuMeasureLoop/tmplWatchLoop/targetProcessLoop), 快捷键, pushWeaponState |
| `gamefocus.go` | `gameProcessNames` (r6s/cs2 进程名), `foregroundGameActive`/`foregroundGameLoop` (前台门控), `targetProcessLoop`/`anyProcessRunning` (目标进程看门狗) |
| `http.go` | HTTP metrics (`/metrics`), 用 encoding/json/v2 + jsontext |
| `utils.go` | `panicIf` |
| `syscall.go` | SetWindowDisplayAffinity / GetWindowDisplayAffinity |
| `constants_debug.go` / `constants_release.go` | `DEBUGGING` 常量, 由 build tag `debug` 切换 |

子包:

| 包 | 职责 |
|---|---|
| `capturer` | `Source` 接口 (Bounds/GetImageTimeout/ProvideMat/FramesElapsed/Close), `Server` 帧循环 (ReadScreen/ReadMat/CloneRgba/RaiseCeiling), DXGI 与 OBS 实现 |
| `wgc` | WGC 采集 (依赖 `wgc_helper.dll`), `FindWindow` (按进程名/标题找窗口), `WgcSource` 窗口/显示器源, 断线重找 (lookup) |
| `matcher` | 模板匹配引擎: Config (Fps/FpsIdle/DropIdleDuration), `ResultCh()` 结果通道, `WEAPON_INDEX_NONE` |
| `weapon` | Weapon 类型与常量: SLOT_*/TYPE_*, `SPEED_SIGN_AUTO ("--")` / `SPEED_SIGN_COPY ("==")`, `ParseFileName` |
| `weapons` | Weapons 集合 (Append/Delete/DeleteByPath/ReadFrom) |
| `detector` | `Source` 接口 (Snapshot/Close), 本地 YOLO 引擎, `RemoteSource` (远程结果, TTL 过期), `Drawer` |
| `assist` | 瞄准辅助引擎 (MoveAndMark), `SetForegroundAllowed` 前台门控 |
| `sender` | WebSocket 推流 (JPEG, `OnResult` 远程检测回调) |
| `remoteclient` | mhub 远程注入客户端 (TCP, 换行分隔 JSON), 实现 `mouse.Mover` |
| `mouse` | `Mover` 接口, `LocalMover` (本地 SendInput), `RawInputTracker` |
| `keystate` | VK 键码, `Tracker` (边沿检测), `IsDown` |
| `ui` / `widgets` | gioui 窗口与绘制, Drawer 接口, 快捷键 |
| `fps` / `timing` / `logger` / `utils` / `contextWaitGroup` | 基础设施: 帧率计数, 耗时诊断, zerolog 封装, 工具, 信号取消的 WaitGroup |
| `cmd/capturebench` | 采集基准工具 (`build.ps1 bench` 产出 capturebench.exe) |

## 构建与测试

必须用 `build.ps1` (内部设置 OpenCV 环境, 见下):

```powershell
.\build.ps1 release          # go build -tags "customenv" -o streamer.exe
.\build.ps1 debug            # -tags "debug,customenv" -gcflags "all=-N -l" (DEBUGGING=true, WGC 边框可见)
.\build.ps1 run              # go run
.\build.ps1 bench            # capturebench.exe
.\build.ps1 wgcdll           # 用 MSVC 编译 wgc_helper.dll (需要 VS2022+, 平时不自动重建)
```

环境要点 (build.ps1 内部设置, 直接 go build 会因缺 OpenCV 头文件失败):
- OpenCV: `B:/Lib/opencv_build/install`, 49 个库, CGO_LDFLAGS 逐个 `-lopencv_<name>4120`
- `CGO_ENABLED=1`, `CGO_CPPFLAGS=-I.../include`, `CGO_CXXFLAGS=--std=c++11 -DNDEBUG`
- `GOEXPERIMENT=nodwarf5`
- 需要 `wgc_helper.dll` 与 `streamer.exe` 同目录 (缺失时 WGC 回退 DXGI)

测试: 直接 `go test ./...` 需要 OpenCV 环境, 建议在 build.ps1 设置的变量下跑。`remoteclient`/`sender` 测试不带 OpenCV 依赖。

## 命令行 flag (main.go)

```
-game r6s|cs2            游戏模式 (进程名/assist 参数来源), 默认 r6s
-source dxgi|obs|wgc|auto 采集源, 默认 auto (DXGI 优先, 失败回退 WGC)
-window <名>|auto        WGC 窗口采集: 进程名/窗口标题, auto=按 -game 进程名
-nogui                   无 GUI 窗口
-noyolo                  禁用本地 YOLO
-noopencv                禁用 OpenCV 模板匹配
-autodisplay             跳过显示器选择, 取最大分辨率
-port :8080 / -nohttp    HTTP metrics
-stream :9090 / -streamfps / -streamquality / -streamcrop / -nosender / -streamttl   WebSocket 推流
-mhub-addr 127.0.0.1:9000  远程注入到 mhub (空=本地注入)
-target-timeout 30m      目标游戏进程 (-game) 连续未检测到该时长则退出自身; 0=禁用
```

## 远程注入协议 (mhub)

`remoteclient` 通过 TCP 换行分隔 JSON 发给 mhub 的 RemoteServer。消息结构 (与 mhub `remoteMsg` 一致):

```go
type remoteMsg struct {
    T      string          `json:"t"`                 // move | click | key | state | expr
    Dx, Dy int32           `json:"dx,omitzero"`        // move 位移
    Mark   bool            `json:"mark,omitzero"`
    B      int             `json:"b,omitzero"`         // 按钮 (mhub 编号 1=左 2=右 3=中)
    Down   bool            `json:"down,omitzero"`
    Clicks int32           `json:"clicks,omitzero"`
    Key    string          `json:"key,omitzero"`
    Value  json.RawMessage `json:"value,omitzero"`     // 任意状态负载
    Expr   string          `json:"expr,omitzero"`
}
```

- `buttonToMhub`: streamer MB_LEFT=0/MIDDLE=1/RIGHT=2 → mhub 1/3/2
- `pushWeaponState` 推 CamelCase key: `WeaponType` (none/full/semi), `FaSpeed`, `FaFrac`, `SaSpeed`, `SaFrac`
- **InjectState 不做命名转换**: 脚本变量名必须与 key 一字不差 (mhub 侧示例见 `mhub-go/examples/scripts/r6s.go`)

## 武器模板

- 目录 `templates/`, 文件名格式 `{CLASS_SPEEDMAIN_SPEEDALT} NAME.png`, 如 `{FA_8.1_--} MK17 CQB.png`
- 速度符号: `--` = auto (*0.7), `==` = copy (*1); `__` 前缀文件忽略 (`TEMPLATES_PREFIX_IGNORE`)
- 热重载: `tmplWatchLoop` 监听 templates/ 目录 (fsnotify), 增删改自动生效, 无需重启
- 快捷键: `Space` 列武器, `Ctrl+Shift+W` 重载模板, `I` 进入调试输入修改武器速度, `B` 切换 debugging

## 目标进程看门狗

`gamefocus.go` 的 `targetProcessLoop` (默认启用, `-target-timeout`):
- 每 5s 枚举进程 (`process.Processes()` + `strings.EqualFold` 匹配 `gameProcessNames`), 检测到即刷新 `lastSeen`
- 连续 `timeout` (默认 30m) 未检测到 → Warn 日志 + `cwg.Cancel()` 优雅退出 (所有循环响应 ctx.Done, defer 清理执行)
- `-target-timeout 0` 或未知 game 模式时禁用

## 代码约定

与兄弟仓库 (mhub-go/mhub-logi/logi-hidpp) 一致, 见各自 AGENTS.md:

### 注释规范

统一使用英文标点, 不使用中文全角标点:
- 句号: 不使用, 直接省略
- 逗号/分号: 用英文 `,` `;`, 后面紧跟文本时加一个空格, 行尾不加
- 冒号: 用英文 `: `, 行尾不加尾随空格
- 引号/括号: 用英文 `"` `()`, 与外部文本隔一个空格, 如 `foo (bar)` `foo "bar", bar`; 引号/括号与逗号之间不加空格
- 函数调用等代码引用保持紧贴, 如 `SleepCtx(ctx, d)`
- 斜杠 `/`: 左右空格不强制

### 命名规范

| 类别 | 风格 | 示例 |
|---|---|---|
| 导出常量 | 全大写蛇形 | `WEAPON_INDEX_NONE`, `SPEED_SIGN_AUTO`, `WDA_EXCLUDEFROMCAPTURE` |
| 内部常量 | 小驼峰 | `debounceInterval`, `lastSeen` |
| 导出变量/函数 | 大驼峰 | `FindWindow`, `NewServer`, `ResultCh` |
| 内部变量 | 小驼峰 | `roiRect`, `capturerServer`, `matcherEngine` |

## 已知注意事项

- `go vet` 在 `wgc/wgc.go` 220/232 行报两处 `possible misuse of unsafe.Pointer` (syscall 句柄转换), 为预先存在问题, 与功能无关, 不要顺手"修复"
- 本地注入模式 (`LocalMover`) 不实现 `statePusher`, pushWeaponState 为 no-op
