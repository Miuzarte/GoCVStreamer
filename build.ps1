param(
    [string]$Command = "run",
    # customenv: 告诉 GoCV 使用自行编译的 OpenCV
    [string]$Tags = "customenv"
    # [string]$Tags = "customenv,matprofile"
)

# 保存执行前的 PATH
$OrigPath = $env:PATH

# 错误处理
$ErrorActionPreference = "Stop"

# 获取当前目录
$ProjectDir = $PSScriptRoot
if (-not $ProjectDir) {
    $ProjectDir = Get-Location
}

# 设置 OpenCV 路径（Windows 格式）
$OpenCVDir = "B:/Lib/opencv_build/install"
# $OpenCVDir = "B:/Lib/opencv_build_msvc_cuda/install"
$OpenCVInclude = Join-Path $OpenCVDir "include"
$OpenCVBin = Join-Path $OpenCVDir "x64\mingw\bin"
# $OpenCVBin = Join-Path $OpenCVDir "x64\vc18\bin"
$OpenCVLib = Join-Path $OpenCVDir "x64\mingw\lib"
# $OpenCVLib = Join-Path $OpenCVDir "x64\vc18\lib"

if (-not (Test-Path $OpenCVDir)) {
    Write-Host "OpenCV 目录不存在: $OpenCVDir" -ForegroundColor Red
    exit 1
}

Write-Host "OpenCV 路径:" -ForegroundColor Yellow
Write-Host "  OpenCV 目录: $OpenCVDir"
Write-Host "  OpenCV Bin: $OpenCVBin"
Write-Host "  OpenCV Include: $OpenCVInclude"
Write-Host "  OpenCV Lib: $OpenCVLib"

# $compiler = "`"B:/Program Files/Microsoft Visual Studio/2022/VC/Tools/MSVC/14.44.35207/bin/Hostx64/x64/cl.exe`""

$OpenCV_VERSION = 4120
$OpenCV_LDFLAGS = `
    # "-lstdc++ " + `
    "-lopencv_stereo$OpenCV_VERSION " + `
    "-lopencv_tracking$OpenCV_VERSION " + `
    "-lopencv_superres$OpenCV_VERSION " + `
    "-lopencv_stitching$OpenCV_VERSION " + `
    "-lopencv_optflow$OpenCV_VERSION " + `
    "-lopencv_gapi$OpenCV_VERSION " + `
    "-lopencv_face$OpenCV_VERSION " + `
    "-lopencv_dpm$OpenCV_VERSION " + `
    "-lopencv_dnn_objdetect$OpenCV_VERSION " + `
    "-lopencv_ccalib$OpenCV_VERSION " + `
    "-lopencv_bioinspired$OpenCV_VERSION " + `
    "-lopencv_bgsegm$OpenCV_VERSION " + `
    "-lopencv_aruco$OpenCV_VERSION " + `
    "-lopencv_xobjdetect$OpenCV_VERSION " + `
    "-lopencv_ximgproc$OpenCV_VERSION " + `
    "-lopencv_xfeatures2d$OpenCV_VERSION " + `
    "-lopencv_videostab$OpenCV_VERSION " + `
    "-lopencv_video$OpenCV_VERSION " + `
    "-lopencv_structured_light$OpenCV_VERSION " + `
    "-lopencv_shape$OpenCV_VERSION " + `
    "-lopencv_rgbd$OpenCV_VERSION " + `
    "-lopencv_rapid$OpenCV_VERSION " + `
    "-lopencv_objdetect$OpenCV_VERSION " + `
    "-lopencv_mcc$OpenCV_VERSION " + `
    "-lopencv_highgui$OpenCV_VERSION " + `
    "-lopencv_datasets$OpenCV_VERSION " + `
    "-lopencv_calib3d$OpenCV_VERSION " + `
    "-lopencv_videoio$OpenCV_VERSION " + `
    "-lopencv_text$OpenCV_VERSION " + `
    "-lopencv_line_descriptor$OpenCV_VERSION " + `
    "-lopencv_imgcodecs$OpenCV_VERSION " + `
    "-lopencv_img_hash$OpenCV_VERSION " + `
    "-lopencv_hfs$OpenCV_VERSION " + `
    "-lopencv_fuzzy$OpenCV_VERSION " + `
    "-lopencv_features2d$OpenCV_VERSION " + `
    "-lopencv_dnn_superres$OpenCV_VERSION " + `
    "-lopencv_dnn$OpenCV_VERSION " + `
    "-lopencv_xphoto$OpenCV_VERSION " + `
    "-lopencv_wechat_qrcode$OpenCV_VERSION " + `
    "-lopencv_surface_matching$OpenCV_VERSION " + `
    "-lopencv_reg$OpenCV_VERSION " + `
    "-lopencv_quality$OpenCV_VERSION " + `
    "-lopencv_plot$OpenCV_VERSION " + `
    "-lopencv_photo$OpenCV_VERSION " + `
    "-lopencv_phase_unwrapping$OpenCV_VERSION " + `
    "-lopencv_ml$OpenCV_VERSION " + `
    "-lopencv_intensity_transform$OpenCV_VERSION " + `
    "-lopencv_imgproc$OpenCV_VERSION " + `
    "-lopencv_flann$OpenCV_VERSION " + `
    "-lopencv_core$OpenCV_VERSION "

# 设置环境变量
$env:PATH = "$OpenCVInclude;$OpenCVBin;$env:PATH"
$env:GOEXPERIMENT = "nodwarf5"
$env:CGO_ENABLED = 1
# $env:CC = $compiler
# $env:CXX = $compiler
$env:CGO_CXXFLAGS = "--std=c++11 -DNDEBUG"
$env:CGO_CPPFLAGS = "-I$OpenCVInclude"
# $env:CGO_LDFLAGS = "-L$OpenCVLib " + $OpenCV_LDFLAGS
$env:CGO_LDFLAGS = "-L$OpenCVLib " + $OpenCV_LDFLAGS

Write-Host "环境变量:" -ForegroundColor Green
Write-Host "  GOEXPERIMENT: $env:GOEXPERIMENT"
Write-Host "  PATH: $OpenCVInclude;$OpenCVBin"
# Write-Host "  CC: $env:CC"
# Write-Host "  CXX: $env:CXX"
Write-Host "  CGO_CXXFLAGS: $env:CGO_CXXFLAGS"
Write-Host "  CGO_CPPFLAGS: $env:CGO_CPPFLAGS"
Write-Host "  CGO_LDFLAGS: $env:CGO_LDFLAGS"

$output = "streamer.exe"
# $output = "streamer_cuda.exe"

$wgcDll = Join-Path $PSScriptRoot "wgc_helper.dll"
if ($Command.ToLower() -ne "wgcdll" -and -not (Test-Path $wgcDll)) {
    Write-Host "wgc_helper.dll 不存在，WGC 采集将回退到 DXGI。需要时先执行: .\build.ps1 wgcdll" -ForegroundColor Yellow
}

# 用本机 MSVC + Windows SDK 编译 wgc_helper.dll（C++/WinRT）。
# 仅在显式执行 "wgcdll" 时构建，release/debug/run 不会自动重建。
function Build-WgcDll {
    $vcvars = "B:\Program Files\Microsoft Visual Studio\18\Community\VC\Auxiliary\Build\vcvars64.bat"
    if (-not (Test-Path $vcvars)) {
        Write-Host "vcvars64.bat not found: $vcvars" -ForegroundColor Red
        exit 1
    }

    $wgcDir = Join-Path $PSScriptRoot "wgc\helper"
    $objDir = Join-Path $env:TEMP "wgc_helper_obj"
    New-Item -ItemType Directory -Force -Path $objDir | Out-Null

    $outDll = Join-Path $PSScriptRoot "wgc_helper.dll"

    Write-Host "Building wgc_helper.dll ..." -ForegroundColor Green

    # 导入 vcvars64.bat 设置的环境变量（INCLUDE/LIB/PATH 等）
    cmd /c "`"$vcvars`" >nul && set" | ForEach-Object {
        if ($_ -match '^([^=]+)=(.*)$') {
            [Environment]::SetEnvironmentVariable($matches[1], $matches[2], "Process")
        }
    }

    & cl /nologo /LD /MT /EHsc /O2 /std:c++17 /D_SILENCE_EXPERIMENTAL_COROUTINE_DEPRECATION_WARNINGS `
        "/Fo:$objDir\" "$wgcDir\wgc_helper.cpp" "/Fe:$outDll" `
        /link "/IMPLIB:$objDir\wgc_helper.lib" windowsapp.lib d3d11.lib dxgi.lib ole32.lib user32.lib dwmapi.lib
    $code = $LASTEXITCODE

    Remove-Item -LiteralPath $objDir -Recurse -Force -ErrorAction SilentlyContinue

    if ($code -ne 0) {
        Write-Host "wgc_helper.dll build failed (exit $code)" -ForegroundColor Red
        exit $code
    }
    Write-Host "wgc_helper.dll -> $outDll" -ForegroundColor Green
}

switch ($Command.ToLower()) {
    "run" {
        Write-Host "go run -tags `"$Tags`"" -ForegroundColor Green
        go run -tags $Tags
        break
    }
    
    "debug" {
        Write-Host "go build -tags `"debug,$Tags`" -gcflags `"all=-N -l`" -o $output" -ForegroundColor Green
        go build -tags "debug,$Tags" -gcflags "all=-N -l" -o $output
        break
    }
    
    "release" {
        Write-Host "go build -tags `"$Tags`" -o $output" -ForegroundColor Green
        go build -tags "$Tags" -o $output
        break
    }

    "bench" {
        Write-Host "go build -tags `"$Tags`" -o capturebench.exe ./cmd/capturebench" -ForegroundColor Green
        go build -tags "$Tags" -o capturebench.exe ./cmd/capturebench
        break
    }

    "wgcdll" {
        Build-WgcDll
        break
    }
    
    default {
        Write-Host "unknown command: $Command" -ForegroundColor Red
        Write-Host "available commands: run, debug, release, bench, wgcdll" -ForegroundColor Yellow
        exit 1
    }
}

# 恢复 PATH
$env:PATH = $OrigPath

exit $LASTEXITCODE
