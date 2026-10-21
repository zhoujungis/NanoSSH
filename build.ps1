# NanoSSH 构建脚本
#
# 职责（把"体积与架构"从口头约定变成可执行断言）：
#   1. 强制 GOARCH=amd64 —— 本机 Go 工具链是 32 位发行版，默认产物是 386，
#      对 SSH 客户端不可接受。只在本进程内设定，不动全局 go env。
#   2. wails build（-s -w 剥符号），随后断言产物确为 amd64。
#   3. 体积守卫：超过阈值直接 throw（P5 现状 13.23MB，阈值默认 14MB 留小余量）。
#   4. govulncheck（若在 PATH 上）——x/crypto 的 advisory 需要可持续发现。
#   5. Authenticode 签名钩子：提供证书才签，否则跳过（不影响产物）。
#
# 用法：
#   .\build.ps1                          # 常规构建 + 体积守卫
#   .\build.ps1 -MaxSizeMB 14            # 自定义体积上限
#   .\build.ps1 -SkipVuln                # 跳过漏洞扫描
#   .\build.ps1 -SignThumbprint <SHA1>   # 用证书库中的代码签名证书签名
#   .\build.ps1 -SignPfx cert.pfx -SignPfxPassword (ConvertTo-SecureString ...)

param(
    [double]$MaxSizeMB = 14.0,
    [switch]$SkipVuln,
    [string]$SignThumbprint = "",
    [string]$SignPfx = "",
    [securestring]$SignPfxPassword
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $root

# ---- 1. 架构钉死：amd64（只影响本进程） ----
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
Write-Host "[build] GOARCH=amd64 CGO_ENABLED=0" -ForegroundColor DarkGray

# ---- 2. 漏洞扫描（可选，先跑：它失败就别出产物） ----
if (-not $SkipVuln) {
    $govuln = Get-Command govulncheck -ErrorAction SilentlyContinue
    if ($govuln) {
        Write-Host "[vuln] govulncheck ./..." -ForegroundColor DarkGray
        & govulncheck ./...
        if ($LASTEXITCODE -ne 0) { throw "govulncheck 发现问题（可用 -SkipVuln 跳过，但不建议）" }
    } else {
        Write-Host "[vuln] 未找到 govulncheck，跳过。安装：go install golang.org/x/vuln/cmd/govulncheck@latest" -ForegroundColor Yellow
    }
}

# ---- 3. 构建 ----
Write-Host "[build] wails build（前端构建由 wails.json 驱动）" -ForegroundColor DarkGray
& wails build -ldflags "-s -w"
if ($LASTEXITCODE -ne 0) { throw "wails build 失败" }

$exe = Join-Path $root "build\bin\NanoSSH.exe"
if (-not (Test-Path $exe)) { throw "未找到产物 $exe" }

# ---- 4. 断言产物架构是 amd64（machine=0x8664）----
$bytes = [IO.File]::ReadAllBytes($exe)
if ($bytes.Length -lt 64) { throw "产物过小，PE 头不完整" }
$peOff = [BitConverter]::ToInt32($bytes, 0x3C)
if ($peOff -le 0 -or $peOff + 6 -gt $bytes.Length) { throw "PE 头偏移异常" }
if ($bytes[$peOff] -ne 0x50 -or $bytes[$peOff + 1] -ne 0x45) { throw "不是 PE 文件" }
$machine = [BitConverter]::ToUInt16($bytes, $peOff + 4)
if ($machine -ne 0x8664) {
    throw ("产物架构错误: machine=0x{0:X4}（期望 0x8664 = amd64）" -f $machine)
}
Write-Host "[arch] machine=0x8664 (amd64) ✓" -ForegroundColor Green

# ---- 5. 体积守卫 ----
$sizeMB = [math]::Round((Get-Item $exe).Length / 1MB, 2)
Write-Host "[size] NanoSSH.exe = $sizeMB MB（上限 $MaxSizeMB MB）" -ForegroundColor Cyan
if ($sizeMB -gt $MaxSizeMB) {
    throw "体积超限：$sizeMB MB > $MaxSizeMB MB。定位是'极致轻'，先查依赖增量再提阈值。"
}

# ---- 6. Authenticode 签名钩子（有证书才签） ----
if ($SignThumbprint -or $SignPfx) {
    $signtool = Get-Command signtool -ErrorAction SilentlyContinue
    if (-not $signtool) {
        $kits = Get-ChildItem "C:\Program Files (x86)\Windows Kits\10\bin\*\x64\signtool.exe" -ErrorAction SilentlyContinue |
            Sort-Object FullName -Descending | Select-Object -First 1
        if ($kits) { $signtool = $kits.FullName }
    }
    if (-not $signtool) { throw "需要签名但找不到 signtool（Windows SDK）" }

    Write-Host "[sign] Authenticode 签名中..." -ForegroundColor DarkGray
    $args = @("sign", "/fd", "SHA256", "/tr", "http://timestamp.digicert.com", "/td", "SHA256")
    if ($SignThumbprint) {
        $args += @("/sha1", $SignThumbprint)
    } else {
        $args += @("/f", $SignPfx, "/p", [Runtime.InteropServices.Marshal]::PtrToStringAuto(
            [Runtime.InteropServices.Marshal]::SecureStringToBSTR($SignPfxPassword)))
    }
    $args += $exe
    & $signtool @args
    if ($LASTEXITCODE -ne 0) { throw "签名失败" }
    Write-Host "[sign] 签名完成 ✓" -ForegroundColor Green
} else {
    Write-Host "[sign] 未提供证书，跳过 Authenticode 签名（-SignThumbprint / -SignPfx 可启用）" -ForegroundColor Yellow
}

Write-Host ""
Write-Host "完成：$exe（$sizeMB MB，amd64）" -ForegroundColor Green
