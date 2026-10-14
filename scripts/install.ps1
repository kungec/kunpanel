# ============================================================================
#  坤面板 KunPanel - Windows 一键安装脚本 (需管理员权限 PowerShell)
#
#  用法:
#    右键"以管理员身份运行" install.bat,或:
#    powershell -NoProfile -ExecutionPolicy Bypass -File install.ps1 [参数]
#
#  参数:
#    (默认)               安装最新版
#    -Version v2.46.4     指定版本
#    -Port 20000          面板端口
#    -Proxy "https://gh-proxy.com/"   GitHub 下载加速
#    -LocalFile xxx.zip   离线安装包
#    -Uninstall           卸载
# ============================================================================
param(
  [string]$Version = "latest",
  [int]$Port = 0,
  [string]$Entrance = "",
  [string]$Proxy = "",
  [string]$LocalFile = "",
  [switch]$Uninstall
)

$ErrorActionPreference = "Stop"
$KP_DIR = "C:\kunpanel"
$SERVICE = "kunpanel"
$REPO_URL = "https://github.com/kungec/kunpanel/releases/download"

function Write-Info($msg)  { Write-Host "[信息] $msg" -ForegroundColor Cyan }
function Write-Ok($msg)    { Write-Host "[成功] $msg" -ForegroundColor Green }
function Write-Warn2($msg) { Write-Host "[警告] $msg" -ForegroundColor Yellow }
function Write-Fail($msg)  { Write-Host "[失败] $msg" -ForegroundColor Red; exit 1 }

# ---------- 管理员权限检查(非管理员自动提权) ----------
$currentUser = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $currentUser.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  Write-Info "需要管理员权限,正在提权 ..."
  $script = $MyInvocation.MyCommand.Definition
  # 完整透传所有参数(此前提权后丢失 Entrance/Proxy/LocalFile)
  $passthru = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "`"$script`"", "-Version", $Version, "-Port", $Port)
  if ($Entrance) { $passthru += @("-Entrance", $Entrance) }
  if ($Proxy) { $passthru += @("-Proxy", "`"$Proxy`"") }
  if ($LocalFile) { $passthru += @("-LocalFile", "`"$LocalFile`"") }
  if ($Uninstall) { $passthru += "-Uninstall" }
  Start-Process powershell -Verb RunAs -ArgumentList ($passthru -join " ")
  exit 0
}

# ---------- 卸载 ----------
if ($Uninstall) {
  Write-Info "停止并卸载坤面板 ..."
  sc.exe stop $SERVICE 2>$null | Out-Null
  sc.exe delete $SERVICE 2>$null | Out-Null
  if (Test-Path $KP_DIR) {
    $ans = Read-Host "是否同时删除数据目录 $KP_DIR(网站/应用数据将丢失)?[y/N]"
    if ($ans -eq "y" -or $ans -eq "Y") { Remove-Item -Recurse -Force $KP_DIR }
  }
  Write-Ok "坤面板已卸载"
  exit 0
}

# ---------- 架构 ----------
$arch = $env:PROCESSOR_ARCHITECTURE
if ($arch -notmatch "AMD64|ARM64") { Write-Warn2 "检测到架构 $arch,将尝试使用 amd64 包" }
$archTag = "amd64"

# ---------- 下载 ----------
New-Item -ItemType Directory -Force -Path "$KP_DIR\bin","$KP_DIR\data" | Out-Null
$tmpZip = "$env:TEMP\kunpanel_$PID.zip"

if ($LocalFile -ne "") {
  if (-not (Test-Path $LocalFile)) { Write-Fail "离线包不存在: $LocalFile" }
  Copy-Item $LocalFile $tmpZip
  Write-Info "使用离线包: $LocalFile"
} else {
  # latest = 直接取最新 release 的无版本产物;指定版本则用带版本产物
  if ($Version -eq "latest") {
    $url = "https://github.com/kungec/kunpanel/releases/latest/download/kunpanel_windows_$archTag.zip"
  } else {
    $url = "$REPO_URL/$Version/kunpanel_${Version}_windows_$archTag.zip"
  }
  if ($Proxy -ne "") { $url = $Proxy + $url }
  Write-Info "下载坤面板 $Version (windows_$archTag) ..."
  Write-Info "地址: $url"
  try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri $url -OutFile $tmpZip -UseBasicParsing -TimeoutSec 300
  } catch { Write-Fail "下载失败: $_" }
}

Write-Info "解压到 $KP_DIR\bin ..."
Expand-Archive -Path $tmpZip -DestinationPath "$KP_DIR\bin" -Force
Remove-Item $tmpZip -Force -ErrorAction SilentlyContinue
if (-not (Test-Path "$KP_DIR\bin\kunpanel.exe")) {
  # 可能在子目录中
  $exe = Get-ChildItem -Path "$KP_DIR\bin" -Recurse -Filter "kunpanel.exe" | Select-Object -First 1
  if ($exe) { Move-Item $exe.FullName "$KP_DIR\bin\kunpanel.exe" -Force } else { Write-Fail "未找到 kunpanel.exe" }
}

# ---------- 端口/入口环境变量(仅首次初始化生效) ----------
$configFile = "$KP_DIR\data\config.json"
if (-not (Test-Path $configFile)) {
  if ($Port -gt 0) { $env:KP_PORT = "$Port" }
  if ($Entrance -ne "") { $env:KP_ENTRANCE = $Entrance }
  Write-Info "初始化配置(端口与后台入口随机生成,可被参数覆盖)..."
  $proc = Start-Process "$KP_DIR\bin\kunpanel.exe" -WorkingDirectory $KP_DIR -PassThru -WindowStyle Hidden
  Start-Sleep -Seconds 3
  Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
}

# ---------- 注册 Windows 服务 ----------
Write-Info "注册 Windows 服务 $SERVICE ..."
sc.exe create $SERVICE binPath= "`"$KP_DIR\bin\kunpanel.exe`"" start= auto DisplayName= "KunPanel Server Panel" | Out-Null
sc.exe description $SERVICE "坤面板 KunPanel - 轻量级服务器运维管理面板" | Out-Null
sc.exe failure $SERVICE reset= 86400 actions= restart/5000/restart/5000/restart/10000 | Out-Null

# 服务以 SYSTEM 运行,数据目录固定到 C:\kunpanel\data
$env:KP_DATA_DIR = "$KP_DIR\data"
sc.exe start $SERVICE | Out-Null
Start-Sleep -Seconds 2
$svcState = (Get-Service $SERVICE -ErrorAction SilentlyContinue).Status
if ($svcState -ne "Running") {
  Write-Warn2 "服务启动异常,尝试直接启动进程 ..."
  Start-Process "$KP_DIR\bin\kunpanel.exe" -WorkingDirectory $KP_DIR
}

# ---------- 防火墙放行 ----------
$finalPort = 20000
if (Test-Path $configFile) {
  try {
    $cfg = Get-Content $configFile -Raw | ConvertFrom-Json
    $finalPort = $cfg.port
  } catch {}
}
Write-Info "放行防火墙端口 $finalPort ..."
netsh advfirewall firewall delete rule name="KunPanel" | Out-Null
netsh advfirewall firewall add rule name="KunPanel" dir=in action=allow protocol=TCP localport=$finalPort | Out-Null

# ---------- 输出结果 ----------
$realVer = (& "$KP_DIR\bin\kunpanel.exe" -Version 2>$null)
if (-not $realVer) { $realVer = $Version }
Write-Host ""
Write-Host "==============================================================" -ForegroundColor Cyan
Write-Ok "坤面板 KunPanel $realVer 安装完成!"
Write-Host "==============================================================" -ForegroundColor Cyan
if (Test-Path "$KP_DIR\data\initial_password.txt") {
  Get-Content "$KP_DIR\data\initial_password.txt" | ForEach-Object { Write-Host "  $_" }
}
Write-Host "  浏览器访问: http://127.0.0.1:$finalPort"
Write-Host "--------------------------------------------------------------"
Write-Host "  数据目录: $KP_DIR\data"
Write-Host "  服务管理: 服务列表中的 kunpanel,或 sc.exe {query|stop|start} $SERVICE"
Write-Host "  卸载:     以管理员运行 install.ps1 -Uninstall"
Write-Host "==============================================================" -ForegroundColor Cyan
Write-Host ""
Read-Host "按回车键关闭窗口"
