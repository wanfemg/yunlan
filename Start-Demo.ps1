[CmdletBinding()]
param([switch]$SkipBuild, [switch]$BackendOnly)

$ErrorActionPreference = 'Stop'
$projectDir = [IO.Path]::GetFullPath($PSScriptRoot)
$runtimeDir = Join-Path $projectDir 'output\demo-runtime'
$backendDir = Join-Path $projectDir 'backend'
$frontendDir = Join-Path $projectDir 'frontend'
$financeDir = Join-Path $projectDir 'finance'
$backendExe = Join-Path $runtimeDir 'siteadmin-demo.exe'
$viteScript = Join-Path $frontendDir 'node_modules\vite\bin\vite.js'
$bundledDir = Join-Path $env:USERPROFILE '.cache\codex-runtimes\codex-primary-runtime\dependencies'
New-Item -ItemType Directory -Path $runtimeDir -Force | Out-Null

function Find-Runtime([string]$name, [string[]]$fallbacks) {
    foreach ($candidate in $fallbacks) {
        if ($candidate -and (Test-Path -LiteralPath $candidate -PathType Leaf)) { return $candidate }
    }
    $command = Get-Command $name -ErrorAction SilentlyContinue
    if ($command -and $command.Source -notlike '*\Microsoft\WindowsApps\*') { return $command.Source }
    throw "Required runtime is missing: $name"
}

function Assert-LocalPort([int]$port, [string]$expectedEntry) {
    $listeners = @(Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue)
    foreach ($listener in $listeners) {
        $owner = Get-CimInstance Win32_Process -Filter "ProcessId = $($listener.OwningProcess)" -ErrorAction SilentlyContinue
        $line = if ($owner) { ([string]$owner.CommandLine).Replace('/', '\') } else { '' }
        if (-not $line.Contains($expectedEntry.Replace('/', '\'))) {
            throw "Port $port is used by another process. No process was stopped."
        }
        Stop-Process -Id $listener.OwningProcess -ErrorAction Stop
    }
}

function Wait-Local([string]$url) {
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        try {
            $response = Invoke-WebRequest -Uri $url -TimeoutSec 2 -UseBasicParsing
            if ($response.StatusCode -eq 200) { return }
        } catch { }
        Start-Sleep -Milliseconds 250
    }
    throw "Local service did not become ready: $url. Check $runtimeDir."
}

$nodeExe = Find-Runtime 'node.exe' @((Join-Path $bundledDir 'node\bin\node.exe'))
$pythonExe = Find-Runtime 'python.exe' @((Join-Path $bundledDir 'python\python.exe'))
$goExe = Find-Runtime 'go.exe' @((Join-Path $env:LOCALAPPDATA 'Temp\siteadmin-go-1.25.14\go\bin\go.exe'))
if (-not (Test-Path -LiteralPath $viteScript)) { throw 'Frontend dependencies are missing. Run pnpm install in frontend first.' }

# Always launch this local dataset independently of any live shell configuration.
$demoEnvironment = @{
    APP_MODE = 'demo'
    LISTEN_HOST = '127.0.0.1'
    PORT = '18080'
    ADMIN_TOKEN = 'siteadmin-preview'
    SITEADMIN_COOKIE_SECURE = 'false'
    TIMEZONE = 'Asia/Shanghai'
    QUOTA_PER_UNIT = '500000'
    DATA_DIR = (Join-Path $runtimeDir 'data')
    SITEADMIN_CONFIG_FILE = (Join-Path $projectDir 'data\connections.json')
    IGNORE_SAVED_CONNECTIONS = 'true'
    STATIC_DIR = (Join-Path $frontendDir 'dist')
    FINANCE_UPSTREAM = 'http://127.0.0.1:8765'
    FINANCE_PROXY_SECRET = 'siteadmin-local-preview-connector-v1'
    NETWORK_HISTORY_COMPLETE = 'true'
    VITE_UI_PREVIEW_ONLY = 'false'
    VITE_API_BASE = ''
    PYTHONIOENCODING = 'utf-8'
    MAIN_DB_DRIVER = ''
    MAIN_DB_DSN = ''
    LOG_DB_DRIVER = ''
    LOG_DB_DSN = ''
    MANAGEMENT_DB_DRIVER = ''
    MANAGEMENT_DB_DSN = ''
    NEWAPI_UPSTREAM = ''
    NEWAPI_ADMIN_TOKEN = ''
    NEWAPI_ADMIN_USER_ID = ''
    PROBE_SHARED_SECRET = ''
    GEOIP_CITY_PATH = ''
    GEOIP_ASN_PATH = ''
    GEOIP_ISP_PATH = ''
    GEOIP_ANONYMOUS_PATH = ''
    IP_INTELLIGENCE_DIR = (Join-Path $projectDir 'data\ip-intelligence')
}
$previousEnvironment = @{}
$started = @()
try {
    foreach ($key in $demoEnvironment.Keys) {
        $previousEnvironment[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
        [Environment]::SetEnvironmentVariable($key, $demoEnvironment[$key], 'Process')
    }
    if (-not $BackendOnly) {
        Assert-LocalPort 5173 $viteScript
        Assert-LocalPort 8765 (Join-Path $financeDir 'demo.py')
    }
    Assert-LocalPort 18080 $backendExe
    if (-not $SkipBuild -or -not (Test-Path -LiteralPath $backendExe)) {
        Push-Location $backendDir
        try {
            & $goExe build -o $backendExe .
            if ($LASTEXITCODE -ne 0) { throw 'Backend build failed.' }
        } finally { Pop-Location }
    }
    if (-not $BackendOnly) {
        $financeProcess = Start-Process -FilePath $pythonExe -ArgumentList @('"' + (Join-Path $financeDir 'demo.py') + '"', '--port', '8765') -WorkingDirectory $financeDir -WindowStyle Hidden -RedirectStandardOutput (Join-Path $runtimeDir 'finance.out.log') -RedirectStandardError (Join-Path $runtimeDir 'finance.err.log') -PassThru
        $started += $financeProcess
        Wait-Local 'http://127.0.0.1:8765/finance/healthz'
    }
    $backendProcess = Start-Process -FilePath $backendExe -WorkingDirectory $backendDir -WindowStyle Hidden -RedirectStandardOutput (Join-Path $runtimeDir 'backend.out.log') -RedirectStandardError (Join-Path $runtimeDir 'backend.err.log') -PassThru
    $started += $backendProcess
    Wait-Local 'http://127.0.0.1:18080/api/health'
    if (-not $BackendOnly) {
        $frontendProcess = Start-Process -FilePath $nodeExe -ArgumentList @('"' + $viteScript + '"', '--mode', 'demo', '--host', '127.0.0.1', '--port', '5173', '--strictPort') -WorkingDirectory $frontendDir -WindowStyle Hidden -RedirectStandardOutput (Join-Path $runtimeDir 'frontend.out.log') -RedirectStandardError (Join-Path $runtimeDir 'frontend.err.log') -PassThru
        $started += $frontendProcess
        Wait-Local 'http://127.0.0.1:5173/api/meta'
    }
    $keptProcesses = @()
    $sessionPath = Join-Path $runtimeDir 'session.json'
    if ($BackendOnly -and (Test-Path -LiteralPath $sessionPath)) {
        $existingSession = Get-Content -LiteralPath $sessionPath -Raw | ConvertFrom-Json
        $keptProcesses = @($existingSession.processes | Where-Object { $_.executable -ne $backendExe })
    }
    $session = @{
        url = 'http://127.0.0.1:5173/'
        mode = 'demo'
        processes = @($keptProcesses) + @($started | ForEach-Object { @{ id = $_.Id; executable = $_.Path } })
        started_at = (Get-Date).ToString('o')
    }
    $session | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $runtimeDir 'session.json') -Encoding utf8
    Write-Host 'Demo ready: http://127.0.0.1:5173/'
    Write-Host 'Demo login: root / 123456'
    Write-Host 'Restart this script to restore the initial dataset.'
} catch {
    foreach ($process in $started) {
        Stop-Process -Id $process.Id -ErrorAction SilentlyContinue
    }
    throw
} finally {
    foreach ($key in $previousEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($key, $previousEnvironment[$key], 'Process')
    }
}
