#requires -Version 5.1
<#
  import-orcaterm.ps1
  从本机 OrcaTerm 导出一个账号的 access_token + refresh_token（JSON）。
  原理：
    1. 读 %APPDATA%\com.orcaterm-desktop.app\data.bin 里的 oauth_access_token / oauth_expires_at / userId；
    2. access token 可直接用；refresh token 存在系统密钥存储里，需要借助运行中的 OrcaTerm 通过 CDP 读取；
    3. 输出整段 JSON，粘贴到 orcaterm2api 面板「添加账号 → 粘贴令牌」即可。

  用法：
    powershell -ExecutionPolicy Bypass -File .\scripts\import-orcaterm.ps1
    powershell -ExecutionPolicy Bypass -File .\scripts\import-orcaterm.ps1 -Port 9333 -KeepRunning
#>
param(
  [int]$Port = 9333,
  [switch]$KeepRunning
)

$ErrorActionPreference = 'Stop'
$appDir = Join-Path $env:LOCALAPPDATA 'OrcaTerm'
$exe = Join-Path $appDir 'OrcaTerm.exe'
$dataBin = Join-Path $env:APPDATA 'com.orcaterm-desktop.app\data.bin'

if (-not (Test-Path $dataBin)) {
  Write-Error "找不到 $dataBin，请先启动并登录 OrcaTerm。"
}

$data = Get-Content $dataBin -Raw -Encoding UTF8 | ConvertFrom-Json
$accessToken = $data.oauth_access_token
if ([string]::IsNullOrWhiteSpace($accessToken)) {
  Write-Error 'data.bin 里没有 oauth_access_token，请在 OrcaTerm 里登录后再试。'
}

$refresh = $null
$started = $null
function Test-Cdp {
  try { Invoke-WebRequest "http://127.0.0.1:$Port/json/version" -UseBasicParsing -TimeoutSec 2 | Out-Null; return $true } catch { return $false }
}

if (-not (Test-Cdp)) {
  if (-not (Test-Path $exe)) {
    Write-Warning "找不到 $exe，跳过 refresh token 读取（仅导出 access token）。"
  } else {
    Write-Host "启动 OrcaTerm（CDP 端口 $Port）…" -ForegroundColor Cyan
    $env:WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS = "--remote-debugging-port=$Port --remote-allow-origins=*"
    $started = Start-Process -FilePath $exe -WorkingDirectory $appDir -PassThru
    for ($i = 0; $i -lt 30; $i++) {
      Start-Sleep -Milliseconds 500
      if (Test-Cdp) { break }
    }
  }
}

if (Test-Cdp) {
  $node = Get-Command node -ErrorAction SilentlyContinue
  if (-not $node) {
    Write-Warning '未安装 Node.js，无法读取系统密钥存储里的 refresh token（仅导出 access token）。'
  } else {
    $script = Join-Path $PSScriptRoot 'cdp_secret.mjs'
    $raw = & $node.Source $script $Port 2>$null
    try {
      $res = $raw | ConvertFrom-Json
      if ($res.ok) { $refresh = $res.refresh_token }
      else { Write-Warning "读取 refresh token 失败：$($res.error)" }
    } catch {
      Write-Warning "解析 CDP 输出失败：$raw"
    }
  }
} else {
  Write-Warning 'OrcaTerm 未以调试模式运行，且启动失败；仅导出 access token。'
}

$nick = ''
if ($data.auth_identity) {
  try { $nick = ($data.auth_identity | ConvertFrom-Json).nickName } catch {}
}

$result = [ordered]@{
  access_token  = $accessToken
  refresh_token = $refresh
  expires_at    = $data.oauth_expires_at
  user_id       = $data.userId
  nick_name     = $nick
  source        = 'local-import'
  exported_at   = [DateTimeOffset]::Now.ToString('o')
}

Write-Host ''
Write-Host '=== 复制下面整段 JSON，粘贴到面板「添加账号 → 粘贴令牌」 ===' -ForegroundColor Green
$result | ConvertTo-Json | Write-Output
Write-Host '===========================================================' -ForegroundColor Green

if ($started -and -not $KeepRunning) {
  Write-Host "已启动的 OrcaTerm 保持运行（PID $($started.Id)）；需要关闭请手动退出。" -ForegroundColor DarkGray
}
if (-not $refresh) {
  Write-Host '提示：没有 refresh token 的账号只能用 2 小时，建议用面板「微信扫码」重新登录一个可刷新账号。' -ForegroundColor Yellow
}
