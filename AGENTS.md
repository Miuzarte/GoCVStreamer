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
-mhub-script RainbowSix   武器状态注入的目标 mhub 脚本名 (空=mhub primary 脚本)
-target-timeout 30m      目标游戏进程 (-game) 连续未检测到该时长则退出自身; 0=禁用
```

## 远程注入协议 (mhub)

`remoteclient` 通过 TCP 换行分隔 JSON 发给 mhub 的 RemoteServer。消息结构 (与 mhub `remoteMsg` 一致):

```go
type remoteMsg struct {
    T      string          `json:"t"`                 // move | btn | wheel | ping | state | eval
    Dx, Dy int32           `json:"dx,omitzero"`        // move 位移
    Mark   bool            `json:"mark,omitzero"`
    B      int             `json:"b,omitzero"`         // 按钮 (mhub 编号 1=左 2=右 3=中)
    Down   bool            `json:"down,omitzero"`
    Clicks int32           `json:"clicks,omitzero"`
    Key    string          `json:"key,omitzero"`
    Value  json.RawMessage `json:"value,omitzero"`     // 任意状态负载
    Expr   string          `json:"expr,omitzero"`
    Target string          `json:"target,omitzero"`    // state/eval 目标脚本名 (空=primary)
}
```

- `buttonToMhub`: streamer MB_LEFT=0/MIDDLE=1/RIGHT=2 → mhub 1/3/2
- `pushWeaponState` 推 CamelCase key: `WeaponType` (none/full/semi), `FaSpeed`, `FaFrac`, `SaSpeed`, `SaFrac`
  - 目标脚本名由 `-mhub-script` 指定 (默认 `RainbowSix`): **不指定时状态会被投给 mhub 当前 primary 脚本**, 也就是"当前前台窗口命中的那个脚本" — 前台不是游戏 (或无脚本命中) 时状态会落空, 压枪静默失效
  - `weaponStateRepushLoop` 每秒重推一次 **`r6sLoop` 已确认的武器状态** (`weaponState`): 识别到武器推该武器, 丢失识别推 `none` 归零 (之前只重推已识别武器, 丢识别后 mhub 侧会永久留旧武器参数); 丢推/mhub 重启/热重载/丢识别都在一秒内自愈; 重推日志为 Trace, 状态变化时才是 Debug
  - **重推不能直接读 `matcherEngine.WeaponIndex()`**: 未匹配帧的 `result` 是 `MatchResult{}` 零值 (`WeaponIndex` 是 Go 零值 0, 不是 `WEAPON_INDEX_NONE`), 且 matcher 还在 `DropIdleDuration` 内, 于是"没识别到"会被误读成第 0 把武器并一直重推它的参数; 归零时机只由 `r6sLoop` 的 `toNoneDebounceInterval` (1.5s) 防抖决定, 重推循环不另起一套判断
  - `SetRemoteState` 失败会打 WARN (`failed to push weapon state to mhub`), 不再静默
- **InjectState 不做命名转换**: 脚本变量名必须与 key 一字不差 (mhub 侧示例见 `mhub-go/examples/scripts/RainbowSix.go`, 其 `var (WeaponType string; FaSpeed int ...)` 与 `pushWeaponState` 推送的 key 一字不差)

## 相关仓库速览 (跨仓库继续工作时参考)

streamer 依赖/联动以下独立仓库 (全部本地路径, 可直接改; 无 git 提交历史要求)。上游到下游: `logi-hidpp ← mhub-logi ← mhub-go ← GoCVStreamer`, plus 远程推理客户端 `Inferencer/goApp`。

### 逆向 / 调试资源 (lghub_agent 与 Logitech 协议)

**IDA MCP 可用**: 有一个 `ida-pro-mcp` HTTP 端点 (`http://127.0.0.1:13337/mcp`), 用 `B:\Programming\C\LghubAgentHooking\ida-mcp.ps1` 驱动**已打开**的 IDA 数据库:
- 命令: `tools`, `call <tool> '<json>'`, `decompile <addr>`, `disasm <addr>`, `find <regex>`, `search <text> [--regex]`, `xrefs_to <addr>`, `analyze <addr>`, `py '<code>'`, `funcs <glob>`, `strings <regex>`, `bytes <addr> <size>` (详看脚本头注释)
- 逆向目标: `C:\Program Files\LGHUB\lghub_agent.exe` (G Hub agent)
- IDA 数据库: `B:\Programming\C\LghubAgentHooking\lghub_agent_2026_4\lghub_agent.exe.i64` (另 `lghub_agent_2025_9` 为旧版对照)
- 已有成果: `re_out/` (反编译 C 源码, 覆盖 device/bus 层), `skills/*.md` (`reverse-device-reconnect.md` / `reverse-button-detection.md` / `reverse-movemouserelative.md` / `reverse-wheel-macro-binding.md`)

**lghub_agent 设备层结论** (devio 命名空间: `Win32Bus` / `Win32Device` / `DeviceManager`):
- **事件驱动 + 按需枚举验证, 无周期全量轮询**: (a) 事件层 `WM_DEVICECHANGE`(接口级 + 句柄级 `DBT_DEVTYP_HANDLE`)+ `RegisterSuspendResumeNotification`; (b) 验证层 命令超时 → 重跑 SetupDi 枚举 → 路径丢失 (zombie)→ `unplug_if_gone` → 等重插 → `onPlug` 全新设备
- **无线鼠标是接收器的子设备 (Centurion/Lightspeed)**: 由 `Feature0009Subdevices` + `Feature0003CentPPBridge` 管理; `Feature0009Subdevices::onFeatureReport` 解析子设备变更报告 (byte0=index, byte1=flags bit7 known / bit6 on, byte2=maxpacket)→ 派发 `SubDeviceChanged` → 重新打开设备并重订阅 0x8110
- 接收器走**经典 HID++ 2.0** (`0x10/0x11` 报文), 不是 Centurion `0x50/0x51` 耳机运输层
- **Solaar** (`B:\Git\Solaar`): 接收器按槽位号 `range(1,8)` 枚举设备 (设备请求用 devnumber=槽位号); 设备连接通知 `data[0]&0x40`=offline; 按键 feature `MOUSE_BUTTON_SPY=0x8110`、`REPROG_CONTROLS=0x1B00..0x1B04`; Centurion 只在 headset 用

**Wireshark MCP** (tshark 包装): 能分析 pcap, 但**抓 USB HID 需装 USBPcap** (否则只有 NPF 网络接口, 抓不到鼠标 USB 帧)

### mhub-go — G Hub 脚本运行时 (`B:\Git\mhub-go`, `github.com/Miuzarte/mhub-go`)

- Go + Yaegi 复刻 G Hub Lua 的鼠标/键盘/宏能力, 脱离 G Hub; 核心是**纯库**, 脚本/宏/配置文件全部由调用方通过 `Options` 传入
- **streamer 当前没有嵌入运行时**, 只是经 `-mhub-addr` 走"远程注入" (remoteclient → RemoteServer, 见上文协议); `docs/mhub-integration.md` 里"嵌入 GetRuntime"是旧做法, 以 `-mhub-addr` 为准
- 独立运行时 (`cmd/mhub`): `mhub.NewRemoteServer(r, *listen)` 起 TCP 监听, `-listen 127.0.0.1:9000`, `-root <目录>` / `-debug`; 构建 `go build -tags logi ./cmd/mhub`, 发布 `.\build.ps1 all` (dist/core 无扩展, dist/logi 只带罗技)
- 运行时 API 是**包级真实函数** (`Sleep`, `StartMacro`, `PressMouseButton`, `OutputLogMessage`, `RegisterHotkey`, `GetTickrate`...), 委托全局 Runtime (Viper 式单例); 未创建 Runtime 时调用返回 `ErrNoDefaultRuntime`; `New` 会注册为全局。脚本里这些名字不能再声明
- 脚本模型: `scripts\*.go` 导出 `NewScript() *Script`, 结构体嵌入 `ScriptMeta` (字段: Enabled/ProcessNames/WindowTitles/**Resident**, 零值默认), 需 `Init() error` + `OnEvent(event Event, arg int, family Family)`, `Close()` 可选; **`OnEvent` 可加第 4 参 `device *DeviceInfo` (含设备名+电量, 每次防御性拷贝), 不再兼容旧的 4 参 string**; **`ProcessNames`/`WindowTitles` 为正则 (大小写不敏感)**; **多脚本并行激活**: 激活集合 = 常驻脚本 + 前台命中脚本, 事件派发给全部激活脚本; **常驻 = 仅 `Resident=true`** (空绑定不再自动常驻); **`Exclusive=true` 独占**: 激活本脚本则关闭其余所有脚本并停止后续匹配; `Enabled` 兜底脚本仅在无前台命中时启用 (独占不参与兜底); **primary(主导) = 激活集合第一个非常驻脚本**, 供 `ActiveScript()` 与默认注入
- **注入目标**: `Runtime.InjectExpr(expr, target...)` / `Runtime.InjectState(key, value, target...)` 可指定目标脚本名 (空 = primary), 远程协议经 `remoteMsg.Target` 透传; 纯表达式注入到非激活脚本仍作废 (记日志, 不报错)
- **状态注入改为"缓存 + 补发"**: 目标未激活 / 无 primary / 目标脚本没有该变量时, key/value 落 `Runtime.stateCache` (Debug 日志, 不再静默丢弃), 脚本激活 (含热重载重建解释器) 时由 `replayStateLocked` 补发; 这修掉了"streamer 推一次时游戏不在前台 → 压枪永久失效"的问题
- 事件类型: `EVENT_PROFILE_ACTIVATED/DEACTIVATED`、`EVENT_MOUSE_BUTTON_PRESSED/RELEASED`、**`EVENT_BATTERY_UPDATE`** (arg=电量百分比, family=mouse, device=*DeviceInfo); `DeviceInfo{Device(路径), Name, Battery *BatteryInfo}`、`BatteryInfo{Percent, Level, Charging, Full}`; 设备信息经 `DeviceInfoFor(device)` 取**防御性拷贝** (深拷贝), 脚本改写不影响 runtime canonical
- 宏模型: `macros\*.go` 导出 `Init()`, 用 `RegisterMacro(name, func(ctx))` 注册; 同名宏同时只跑一个, `StartMacro/StopMacro` 幂等; 取消必须监听 `ctx.Done()` (用 `SleepCtx(ctx, d)`); **别用被点击键做触发或门控** (注入的点击进入同一输入流, `IsMouseButtonPressed` 读 GetAsyncKeyState 会被自己的 LEFTUP 带偏)
- 设备扩展: 只定义接口 `DeviceExtension` (`Start(ctx)` / `Events()` / `ButtonDown(button)` / `Close()`), 不依赖厂商代码; 扩展包 `init` 里 `mhub.RegisterExtension(name, factory)`, 宿主空白导入即生效; `Options.DeviceExtensions` (白名单, 空=全部) / `DeviceExtensionSettings` (按名传原始 JSON) / `DisableDeviceExtensions` (总开关)
- 注入标记: 移动带 `Mark` 时调 `MoveRelativeExtra(dx, dy, remoteExtraInfo)`, `remoteExtraInfo = 0x474F4356` (与 streamer `mouse.OurMouseExtraInfo` 约定, rawinput 靠它区分"自己注入的移动" vs "用户真实移动")
- `InjectState(key, value)` 走 `stateAssignExpr` 生成 `key = <literal>` 赋值 (key 必须是合法标识符); `InjectExpr` 异步进事件循环, **不做命名转换**, 变量名须与 key 一字不差
- 热重载: fsnotify 监听 `scripts/`、`macros/`、`config.json`, 防抖 300ms 后全量重建 (取消旧 goroutine→重载宏/脚本→保留 M 键状态与配置→按当前前台窗口重新激活); `Options.DisableWatch: true` 关闭; 手动 `r.Reload()`
- run 用 Go 1.27.0, go.mod replace `mhub-logi`/`logi-hidpp` 到本地

### mhub-logi — 罗技设备扩展 (`B:\Git\mhub-logi`, `github.com/Miuzarte/mhub-logi`)

- 注册名 `logitech`; 单独 module, go 1.27.0; 包装 `logihidpp.Monitor`, 把 HID++ 0x8110 位图事件翻译成**逻辑按键事件 (6..11)**; 侧键 bit 硬编码在 `mapping.go`
- 映射 (`mapping.go`): `BIT_G6=Bit4(逻辑6拇指尖)`, `BIT_WHEEL_LEFT=Bit6(7滚轮左倾)`, `BIT_WHEEL_RIGHT=Bit7(8滚轮右倾)`, `BIT_G9=Bit8(9滚轮下方/Profile)`, `BIT_G8=Bit9(10前狙击键·远)`, `BIT_G7=Bit10(11后狙击键·近)`; 标准键 1..5 (含拇指侧键 4/5) 由 WH_MOUSE_LL 派发, 不走 0x8110
- 配置 `{"pid":<PID>}` (如 `0xC547`) 指定鼠标 PID, `"path":<路径子串>` 只选特定接收器; **pid=0 且 path 空 = 监视所有匹配接收器** (不再只选第一个), 每个接收器一条独立 0x8110 连接, 事件合并 (`-logi-pid`/`-logi-path` 命令行 flag 同名)
- **设备 ID 透传 (已升级为 DeviceInfo)**: 每个事件带 `mhub.DeviceEvent{..., Info *DeviceInfo}` (`DeviceInfo{Device(路径), Name, Battery}`); runtime 经 `updateDeviceInfo` 存 canonical 快照, `DeviceInfoFor(device)` 取防御性拷贝给脚本 `OnEvent` 第 4 参; 仅扩展/侧键/电池事件带 device, 标准键走全局钩子 → device 为空
- **电量事件 (已接入 2026-09-06)**: mhub-logi 同时消费 `Monitor.BatteryEvents()`, 每个电池状态更新产出 `mhub.DeviceEvent{Kind: EVENT_BATTERY_UPDATE, Family: MOUSE, Button: BPercent, Info{Name, Battery}}`; `Battery *BatteryInfo{Percent, Level(critical/low/good/full), Charging, Full}`; **Name 优先取 logi-hidpp 读取的真实设备名 (getDeviceName 0x0005, 如 "PRO X SUPERLIGHT"), 读不到才回退设置里的 `"name"` (cmd/mhub `-logi-name`)**; **读不到电量 = 子设备休眠, 不算错误** (Battery 为 nil, 唤醒后经 rebuildPath 重读)
- 校准: `cd B:\Git\mhub-logi && go run ./cmd/mhubcalib -list` / `go run ./cmd/mhubcalib` (逐键按, 输出 bit 建议), 回填 `mapping.go`
- **唤醒重订阅 (已实现 2026-09-06)**: 接收器在子设备断电/上电时广播 7 字节连接通知 (`10 01 41 11 62/72`=off, `a2/b2`=on, 判 `report[4]&0x80`); logi-hidpp 逐接收器常驻读通知, 收 on 即 `rebuildPath` 重建该连接并重订 0x8110 (毫秒级恢复)。见 logi-hidpp 一节
- streamer **目前不直接依赖 mhub-logi**: 无嵌入运行时 (main.go 未 import), 只经 `-mhub-addr` 远程注入, 罗技扩展在 mhub 以独立运行时 (`cmd/mhub`) 运行时才生效; 若将来在宿主内嵌 mhub, 才需空白导入并按需设 `Options.DeviceExtensions` / `DisableDeviceExtensions` (见 mhub-go 一节)

### logi-hidpp — HID++ 协议 (`B:\Git\logi-hidpp`, `github.com/Miuzarte/logi-hidpp`)

- 只负责读 Logitech HID++ 鼠标按键**位图**, 不感知 keystate; Windows-only, 仅依赖 `golang.org/x/sys`; 独立 module, go 1.27.0
- 关键类型: `Monitor` (`Start`/`OpenWithPID`/`OpenWithFilter(ctx, pid, pathSub)` → 多接收器并集 / `State()`(所有设备位图 OR)/`Events()`(合并)/`Batteries()`(路径→电量快照)/`BatteryEvents()`(电量事件流)/`DeviceName(path)`(设备名, getDeviceName 0x0005 缓存)/`Close()`), `Bit` (0..15), `Event{Bit, Pressed, Device}` (Device=来源接收器路径), `BatteryStatus{Device, Percent, Level, Charging, Full, Source}` (Device 由 Monitor 填, 区分设备)
- `device_info.go`: `DeviceInfo` (Path/VID/PID/UsagePage/Usage/InputReportByteLength), `-list` 只读枚举
- 0x8110 Mouse Button Spy: `IRoot.getFeature(0x8110)` 动态拿 feature index (禁止硬编码) → `startSpy()`; 长报告 20 字节, `data[0]==0x11 && data[2]==featIdx && data[3]==0x00` 时更新位图 (`uint16(data[4])<<8 | uint16(data[5])`); 报告是当前按住状态, 按下/抬起靠前后位图做边沿检测
- 枚举过滤: VID `0x046D`, UsagePage `0xFF00`, Usage∈`{0x0001(短,7B), 0x0002(长,20B)}`; 无线接收器是槽位号, **不要按 device byte `0xFF` 过滤**
- `g502x.go`: 本机校准位别名 `G502X_G6=Bit4` / `WHEEL_LEFT=Bit6` / `WHEEL_RIGHT=Bit7` / `G9=Bit8` / `G8_SNIPER=Bit9` / `G7_SNIPER=Bit10`
- Monitor 自动重连/重新 startSpy, 读失败先位图清零再重枚举 (鼠标上电/重插回 onboard); 打开失败 (如 GHub 独占) 每 2s 重试仅告警
- **多接收器监控 (2026-09 改)**: Monitor 改为 `reconcile` 对每个匹配接收器开一条独立 0x8110 连接 (`conn` 结构, 每个独立 readLoop 喂同一 `Events()`), 设备插拔/读错误/周期重扫触发增删重建; 不再只连第一个 getFeature 成功的设备; `pathSub` 非空则只按 HID 路径子串过滤
- **`firstReport`/`lastReport` 只在收到真正 0x8110 事件时更新**; ghost 实例靠连接/子设备通知"续命"不再能解除 5 分钟兜底看门狗; `unmatched` 日志带 `(device, feature, func, data)` 并把 subdevice index 纳入签名, 不漏掉子设备变更通知
- **通知驱动的唤醒重订阅 (2026-09-06)**: 接收器在子设备断电/上电时广播 7 字节连接通知 (`10 01 41 11 62/72`=off, `a2/b2`=on, 判 `report[4]&0x80`)。`device` 接口加 `ReadShortReport`/`IsSingleCollection`; `ParseSubdeviceNotification` 解析通知; Monitor 改为 **per-receiver 常驻 watcher** (`readLoop` 单集合 / `readLoop+notifyLoop` 非单集合), 收 on → `requestResub` → `rebuildPath` 重开句柄 + getFeature + startSpy; getFeature 超时 (子设备休眠) 也建 `feat=0` await 连接等 on, 不再有 2s 紧重试循环。`report.go` 常量 `connFeatureIdx=0x41`/`connFuncID=0x11`
- **电量监控 (Unified Battery 0x1004, 2026-09-06 实现+真机验证)**: G502X/GPW (C547) 支持 **feature 0x1004** (非 0x1000/0x1001)。它是**事件流 + 启动一次主动查询**: `BatteryFeatureIndex()` 经 getFeature(0x1004) 拿索引, `QueryBattery(idx)` 发 getStatus (wire function byte `0x10|0x0A=0x1A`) 读一次; 之后设备在电量/充电状态变化时推送 status 事件, readLoop 命中 `report[2]==batteryFeat` 即更新。报告布局 (long, params 从 data[4] 起): `data[4]=state_of_charge 百分比, data[5]=电量等级位域(0x01 critical/0x02 low/0x04 good/0x08 full), data[6]=charging_status(0 放电/1 充电/2 慢充/3 满/4 错误), data[7]=external_power`。`ParseBattery1004(report, idx)` → `BatteryStatus{Percent, Level, Charging, Full, Source}`; `Monitor.Batteries()` (路径→快照) / `Monitor.BatteryEvents()` (事件流)。**0x1FC1 (临时 DPI) 在两台设备上不存在** (getFeature 超时), 所以高频 DPI shift 只能用 0x8100 `SetActiveProfileResolution` (单条指令, 非全量板载写)。`cmd/hidpdump -feat 1000,1004,1FC1` 可探测 feature 有无
- `cmd/hidpdump` / `cmd/hidppcalib` (校准工具); streamer 的 keystate 是 `HIDPPButtonBase=0x1000` 起的 `KeyCode` 适配层 (见 `docs/hidpp-mouse-buttons.md`)

### Inferencer/goApp — Termux 直连 QNN HTP 跑 YOLO (`B:\Git\Inferencer\goApp`, 模块名 `Inferencer`)

- 在 Termux (非 root, SELinux Enforcing) 用 Go 直接调 QNN SDK C API, 加载官方 ONNX 内嵌 EPContext 二进制 (`models/ctx_v73.bin` / `ctx_v81.bin`) 跑 YOLO26n; 作为 **GoCVStreamer 的远程推理客户端** (WebSocket)
- 单文件二进制约 4.5MB, 无 JVM/无 ORT; 小米 13 (SD8Gen2/HTP v73) 单帧 19~23ms; go 1.26, deps 仅 `github.com/coder/websocket` + `github.com/rs/zerolog`
- 目录: `cmd/{extract_ctx,infer,streamer}`, `qnn/` (cgo dlopen + QNN provider vtable), `yolo/` (letterbox/quant + argmax/NMS), `jpeg/` (快路径解码), `logger/logging`, `models/` (EPContext, onnx 不入库), `qnn-headers/` (SDK 头文件不入库), `scripts/` (部署库)
- 流式协议 (与 flutterApp/androidApp 一致): 收 `[4B frame_id LE][JPEG]` (640×640), 回 `{"frame_id":N,"detections":[{"x1","y1","x2","y2","score","class","class_name"}],"inference_ms":M}`; 与 streamer `sender.RemoteDetection/RemoteResult` 同构, 经 `Server.OnResult` → `detector.RemoteSource.SetResults` 合并进本地结果
- `-class 0` (person), `-conf 0.45`, `-affinity N` (固定推理线程到超大核, 忙等重试, 断线重连后自动重绑), `-bench` (各阶段耗时), `-v`
- 关键坑位: vendor 库复制到 `$PREFIX/lib` 并按依赖序预加载 `libvmmem.so → vendor.qti.hardware.dsp@1.0.so → libcdsprpc.so`, 否则 fastrpc 失败; QNN graph I/O 是 quint16, 需量化/反量化 (输入 `scale=1/65536`, 输出 `≈0.010455`, offset 0); execute 必须带 binaryInfo tensor id 否则 error 6004; 直连输出是 640 尺度像素, 归一化别再 `*640`; Termux 下 `QnnContext_free` 触发 SIGABRT, 销毁跳过 QNN free; 退出直接 `os.Exit` 跳过 QNN 清理
- Android 调度器空闲收窄可调度核集 (常 `0x5f` 不含 CPU7), 持续负载约 1s 后放开到 `0xff` 才绑成功; 锁 CPU 用 `runtime.LockOSThread` 再 `sched_setaffinity(pid=0)`

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
