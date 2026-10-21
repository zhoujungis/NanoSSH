# NanoSSH MSI 打包脚本（WiX Toolset 3.x）
#
# 把 build\bin\NanoSSH.exe 打成 perMachine x64 MSI 安装包。
# 版本号必须是 MSI 可接受的数字三段（x.y.z）；带 v 前缀或后缀的输入会被规整。
#
# 用法：
#   .\build\windows\make-msi.ps1 -Version 0.1.0
#   .\build\windows\make-msi.ps1 -Version v0.2.0-dev        # 规整为 0.2.0
#   .\build\windows\make-msi.ps1 -Version 0.1.0 -ExePath D:\out\NanoSSH.exe
#
# 依赖：WiX Toolset 3.x（candle/light）。找不到时尝试 choco install wixtoolset。

param(
    [string]$Version = "0.1.0",
    [string]$ExePath = "",
    [string]$OutDir = ""
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)   # build\windows → 项目根
Set-Location $root

# ---- 版本规整：MSI 的 ProductVersion 只接受数字 x.y.z ----
if ($Version -notmatch '(\d+\.\d+\.\d+)') {
    throw "版本号必须包含数字三段 x.y.z（MSI 限制），收到：$Version"
}
$v = $Matches[1]

if (-not $ExePath) { $ExePath = Join-Path $root "build\bin\NanoSSH.exe" }
if (-not (Test-Path $ExePath)) { throw "未找到 $ExePath（先跑 .\build.ps1）" }
if (-not $OutDir) { $OutDir = Join-Path $root "build\bin" }
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

# ---- 定位 WiX ----
function Find-WixBin {
    $cmd = Get-Command candle.exe -ErrorAction SilentlyContinue
    if ($cmd) { return Split-Path -Parent $cmd.Source }
    if ($env:WIX -and (Test-Path (Join-Path $env:WIX "bin\candle.exe"))) { return (Join-Path $env:WIX "bin") }
    $dirs = Get-ChildItem "C:\Program Files (x86)\WiX Toolset v*\bin" -Directory -ErrorAction SilentlyContinue |
        Sort-Object Name -Descending
    foreach ($d in $dirs) {
        if (Test-Path (Join-Path $d.FullName "candle.exe")) { return $d.FullName }
    }
    return $null
}

$wixBin = Find-WixBin
if (-not $wixBin) {
    Write-Host "[msi] 未找到 WiX Toolset，尝试 choco install wixtoolset ..." -ForegroundColor Yellow
    $choco = Get-Command choco -ErrorAction SilentlyContinue
    if (-not $choco) {
        throw "需要 WiX Toolset 3.x：安装 https://wixtoolset.org 或执行 choco install wixtoolset"
    }
    & choco install wixtoolset -y --no-progress
    if ($LASTEXITCODE -ne 0) { throw "choco 安装 WiX Toolset 失败" }
    $wixBin = Find-WixBin
    if (-not $wixBin) { throw "WiX 安装完成但仍找不到 candle.exe（可能需要重开终端）" }
}

$candle = Join-Path $wixBin "candle.exe"
$light = Join-Path $wixBin "light.exe"
$wxs = Join-Path $PSScriptRoot "NanoSSH.wxs"
$objDir = Join-Path $OutDir "wix"
New-Item -ItemType Directory -Force -Path $objDir | Out-Null
$wixobj = Join-Path $objDir "NanoSSH.wixobj"
$msi = Join-Path $OutDir "NanoSSH-$v-x64.msi"

Write-Host "[msi] WiX: $wixBin" -ForegroundColor DarkGray
Write-Host "[msi] 版本: $v" -ForegroundColor DarkGray

$iconPath = Join-Path $root "build\windows\icon.ico"
$candleArgs = @(
    "-nologo",
    "-arch", "x64",
    "-dProductVersion=$v",
    "-dExePath=$ExePath",
    "-dIconPath=$iconPath",
    "-out", $wixobj,
    $wxs
)
& $candle $candleArgs
if ($LASTEXITCODE -ne 0) { throw "candle 失败" }

& $light -nologo -out $msi $wixobj
if ($LASTEXITCODE -ne 0) { throw "light 失败" }

if (-not (Test-Path $msi)) { throw "未生成 $msi" }
$sizeMB = [math]::Round((Get-Item $msi).Length / 1MB, 2)
Write-Host "[msi] $msi（$sizeMB MB）" -ForegroundColor Green
