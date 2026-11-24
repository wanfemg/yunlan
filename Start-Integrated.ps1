[CmdletBinding()]
param(
    [ValidateSet('live', 'demo')][string]$Mode = 'live',
    [switch]$SkipBuild,
    [switch]$WithoutFrontend,
    [switch]$EnableProbe,
    [switch]$SkipDependencies,
    [string]$EnvFile = (Join-Path $PSScriptRoot '.env')
)

$ErrorActionPreference = 'Stop'
if ($Mode -eq 'demo') {
    & (Join-Path $PSScriptRoot 'Start-Demo.ps1') -SkipBuild:$SkipBuild
    exit $LASTEXITCODE
}

$projectDir = [IO.Path]::GetFullPath($PSScriptRoot)
$runtimeDir = Join-Path $projectDir 'output\integrated-runtime'
$backendDir = Join-Path $projectDir 'backend'
$frontendDir = Join-Path $projectDir 'frontend'
$financeDir = Join-Path $projectDir 'finance'
$probeDir = Join-Path $projectDir 'login-probe'
$backendExe = Join-Path $runtimeDir 'siteadmin-integrated.exe'
$probeExe = Join-Path $runtimeDir 'login-probe.exe'
$financeEntry = Join-Path $financeDir 'run.py'
$viteEntry = Join-Path $frontendDir 'node_modules\vite\bin\vite.js'
$bundledDir = Join-Path $env:USERPROFILE '.cache\codex-runtimes\codex-primary-runtime\dependencies'
$sessionPath = Join-Path $runtimeDir 'session.json'
$secretPath = Join-Path $runtimeDir 'machine-secrets.json'
New-Item -ItemType Directory -Path $runtimeDir -Force | Out-Null

function Find-IntegratedRuntime([string]$name, [string[]]$fallbacks) {
    foreach ($candidate in $fallbacks) {
        if ($candidate -and (Test-Path -LiteralPath $candidate -PathType Leaf)) { return $candidate }
    }
    $command = Get-Command $name -ErrorAction SilentlyContinue
    if ($command -and $command.Source -notlike '*\Microsoft\WindowsApps\*') { return $command.Source }
    throw "Required runtime is missing: $name"
}

function Write-IntegratedPrivateJSON([string]$path, $value) {
    $temporary = $path + '.' + [Guid]::NewGuid().ToString('N') + '.tmp'
    try {
        [IO.File]::WriteAllText($temporary, ($value | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
        $acl = Get-Acl -LiteralPath $temporary
        $acl.SetAccessRuleProtection($true, $false)
        $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
        $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($sid, 'FullControl', 'Allow'))
        Set-Acl -LiteralPath $temporary -AclObject $acl
        Move-Item -LiteralPath $temporary -Destination $path -Force
    } finally {
        if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force }
    }
}

function Assert-IntegratedFreePort([int]$port) {
    if (@(Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue).Count) {
        throw "Port $port is occupied. Stop the existing workspace service first; no process was stopped."
    }
}

function Wait-IntegratedHTTP([string]$url, $process) {
    for ($attempt = 0; $attempt -lt 80; $attempt++) {
        $process.Refresh()
        if ($process.HasExited) { throw "A service exited during startup. Check $runtimeDir." }
        try {
            $response = Invoke-WebRequest -Uri $url -TimeoutSec 2 -UseBasicParsing
            if ($response.StatusCode -eq 200) { return }
        } catch { }
        Start-Sleep -Milliseconds 250
    }
    throw "Service did not become ready: $url. Check $runtimeDir."
}

function Start-IntegratedChild([string]$name, [string]$executable, [string[]]$arguments,
                              [string]$directory, [string]$entry) {
    $options = @{
        FilePath = $executable; WorkingDirectory = $directory; WindowStyle = 'Hidden'; PassThru = $true
        RedirectStandardOutput = (Join-Path $runtimeDir ($name + '.out.log'))
        RedirectStandardError = (Join-Path $runtimeDir ($name + '.err.log'))
    }
    if ($arguments.Count) { $options.ArgumentList = $arguments }
    $process = Start-Process @options
    return @{ process = $process; name = $name; entry = $entry; executable = $executable }
}

$allowedKeys = @(
    'APP_MODE', 'ADMIN_TOKEN', 'MAIN_DB_DRIVER', 'MAIN_DB_DSN', 'LOG_DB_DRIVER', 'LOG_DB_DSN',
    'MANAGEMENT_DB_DRIVER', 'MANAGEMENT_DB_DSN', 'NEWAPI_UPSTREAM', 'NEWAPI_ADMIN_TOKEN',
    'NEWAPI_ADMIN_USER_ID', 'PROBE_SHARED_SECRET', 'PROBE_TRUSTED_PROXIES', 'LOGIN_PROBE_ENABLED',
    'FINANCE_PROXY_SECRET', 'GEOIP_CITY_PATH', 'GEOIP_ASN_PATH', 'GEOIP_ISP_PATH',
    'GEOIP_ANONYMOUS_PATH', 'IP_INTELLIGENCE_DIR', 'NETWORK_HISTORY_COMPLETE', 'DATA_DIR',
    'TIMEZONE', 'QUOTA_PER_UNIT', 'SITEADMIN_CONFIG_FILE', 'SITEADMIN_COOKIE_SECURE'
)
$effective = @{}
foreach ($key in $allowedKeys) { $effective[$key] = [Environment]::GetEnvironmentVariable($key, 'Process') }
if (Test-Path -LiteralPath $EnvFile -PathType Leaf) {
    foreach ($line in [IO.File]::ReadAllLines([IO.Path]::GetFullPath($EnvFile))) {
        if ($line -notmatch '^\s*([A-Z][A-Z0-9_]*)\s*=(.*)$') { continue }
        $key = $matches[1]
        if ($key -notin $allowedKeys) { continue }
        $value = $matches[2].Trim()
        if ($value.Length -ge 2 -and (($value.StartsWith('"') -and $value.EndsWith('"')) -or
            ($value.StartsWith("'") -and $value.EndsWith("'")))) { $value = $value.Substring(1, $value.Length - 2) }
        if ([string]::IsNullOrWhiteSpace($effective[$key])) { $effective[$key] = $value }
    }
}
$effective.APP_MODE = 'live'
$dataDir = if ($effective.DATA_DIR) { $effective.DATA_DIR } else { Join-Path $projectDir 'data' }
if (-not [IO.Path]::IsPathRooted($dataDir)) { $dataDir = Join-Path $projectDir $dataDir }
$effective.DATA_DIR = [IO.Path]::GetFullPath($dataDir)
$connectionsPath = if ($effective.SITEADMIN_CONFIG_FILE) { $effective.SITEADMIN_CONFIG_FILE } else { Join-Path $effective.DATA_DIR 'connections.json' }
if (-not [IO.Path]::IsPathRooted($connectionsPath)) { $connectionsPath = Join-Path $projectDir $connectionsPath }
$connectionsPath = [IO.Path]::GetFullPath($connectionsPath)
$effective.IGNORE_SAVED_CONNECTIONS = 'false'
$savedValues = @{}
if (Test-Path -LiteralPath $connectionsPath) {
    $settings = Get-Content -LiteralPath $connectionsPath -Raw | ConvertFrom-Json
    foreach ($property in $settings.values.PSObject.Properties) { $savedValues[$property.Name] = [string]$property.Value }
}
function Get-IntegratedSetting([string]$key) {
    if ($savedValues.ContainsKey($key)) { return $savedValues[$key] }
    return $effective[$key]
}
if ((Get-IntegratedSetting 'APP_MODE') -ne 'live') {
    throw 'Saved connections still use demo mode. Set live mode in /system and restart, or use Start-Demo.ps1.'
}
if (-not (Get-IntegratedSetting 'MAIN_DB_DSN')) {
    throw 'Live mode needs MAIN_DB_DSN in .env or data/connections.json. Configure /system in demo mode first.'
}

$storedSecrets = @{}
if (Test-Path -LiteralPath $secretPath) {
    $document = Get-Content -LiteralPath $secretPath -Raw | ConvertFrom-Json
    foreach ($property in $document.PSObject.Properties) { $storedSecrets[$property.Name] = [string]$property.Value }
}
foreach ($key in @('ADMIN_TOKEN', 'FINANCE_PROXY_SECRET', 'PROBE_SHARED_SECRET')) {
    if ([string]::IsNullOrWhiteSpace($effective[$key])) {
        if (-not $storedSecrets[$key]) {
            $bytes = New-Object byte[] 32
            $random = [Security.Cryptography.RandomNumberGenerator]::Create()
            try { $random.GetBytes($bytes) } finally { $random.Dispose() }
            $storedSecrets[$key] = ([BitConverter]::ToString($bytes)).Replace('-', '').ToLowerInvariant()
        }
        $effective[$key] = $storedSecrets[$key]
    }
}
Write-IntegratedPrivateJSON $secretPath $storedSecrets

$probeEnabled = $EnableProbe -or $effective.LOGIN_PROBE_ENABLED -in @('1', 'true', 'yes', 'on')
if ($probeEnabled -and -not (Get-IntegratedSetting 'NEWAPI_UPSTREAM')) {
    throw 'Login probe requires NEWAPI_UPSTREAM. No service was started.'
}
$nodeExe = Find-IntegratedRuntime 'node.exe' @((Join-Path $bundledDir 'node\bin\node.exe'))
$pythonExe = Find-IntegratedRuntime 'python.exe' @((Join-Path $bundledDir 'python\python.exe'))
$goExe = Find-IntegratedRuntime 'go.exe' @((Join-Path $env:LOCALAPPDATA 'Temp\siteadmin-go-1.25.14\go\bin\go.exe'))
if (-not $WithoutFrontend -and -not (Test-Path -LiteralPath $viteEntry)) {
    throw 'Frontend dependencies are missing. Run pnpm --dir frontend install --frozen-lockfile.'
}
if ($WithoutFrontend -and -not $SkipBuild) {
    if (-not (Test-Path -LiteralPath $viteEntry)) { throw 'Frontend dependencies are missing. Run pnpm --dir frontend install --frozen-lockfile.' }
    Push-Location $frontendDir
    try {
        & $nodeExe $viteEntry build
        if ($LASTEXITCODE -ne 0) { throw 'Frontend build failed.' }
    } finally { Pop-Location }
}
if ($WithoutFrontend -and -not (Test-Path -LiteralPath (Join-Path $frontendDir 'dist\index.html'))) {
    throw 'Built frontend is missing; rerun without -SkipBuild.'
}
Assert-IntegratedFreePort 18080
Assert-IntegratedFreePort 8765
if (-not $WithoutFrontend) { Assert-IntegratedFreePort 5173 }
if ($probeEnabled) { Assert-IntegratedFreePort 18081 }
if (-not $SkipDependencies) {
    & $pythonExe -m pip install --disable-pip-version-check -r (Join-Path $financeDir 'requirements.txt')
    if ($LASTEXITCODE -ne 0) { throw 'Finance dependency installation failed.' }
}
if (-not $SkipBuild -or -not (Test-Path -LiteralPath $backendExe)) {
    Push-Location $backendDir
    try {
        & $goExe build -o $backendExe .
        if ($LASTEXITCODE -ne 0) { throw 'Backend build failed.' }
    } finally { Pop-Location }
}
if ($probeEnabled -and (-not $SkipBuild -or -not (Test-Path -LiteralPath $probeExe))) {
    Push-Location $probeDir
    try {
        & $goExe build -o $probeExe .
        if ($LASTEXITCODE -ne 0) { throw 'Login probe build failed.' }
    } finally { Pop-Location }
}

$effective.LISTEN_HOST = '127.0.0.1'
$effective.PORT = '18080'
$effective.STATIC_DIR = Join-Path $frontendDir 'dist'
$effective.FINANCE_UPSTREAM = 'http://127.0.0.1:8765'
$effective.SITEADMIN_INTERNAL_URL = 'http://127.0.0.1:18080'
$effective.SITEADMIN_CONFIG_FILE = $connectionsPath
$effective.SITEADMIN_ORIGIN = 'http://127.0.0.1:18080'
$effective.PROBE_LISTEN = '127.0.0.1:18081'
$effective.PROBE_DATA_DIR = Join-Path $effective.DATA_DIR 'login-probe'
$effective.UI_BIND = '127.0.0.1'
$effective.UI_PORT = '8765'
$effective.UI_BASE_PATH = '/finance'
$effective.VITE_UI_PREVIEW_ONLY = 'false'
$effective.VITE_API_BASE = ''
$effective.PYTHONIOENCODING = 'utf-8'
$effective.PYTHONDONTWRITEBYTECODE = '1'
if (-not $effective.IP_INTELLIGENCE_DIR) { $effective.IP_INTELLIGENCE_DIR = Join-Path $projectDir 'data\ip-intelligence' }
if (-not $effective.TIMEZONE) { $effective.TIMEZONE = 'Asia/Shanghai' }
$effective.TZ = $effective.TIMEZONE
$previousEnvironment = @{}
$started = @()
try {
    foreach ($key in $effective.Keys) {
        $previousEnvironment[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
        [Environment]::SetEnvironmentVariable($key, $effective[$key], 'Process')
    }
    $backend = Start-IntegratedChild 'backend' $backendExe @() $backendDir $backendExe
    $started += $backend
    Wait-IntegratedHTTP 'http://127.0.0.1:18080/api/health' $backend.process
    [Environment]::SetEnvironmentVariable('DATA_DIR', (Join-Path $effective.DATA_DIR 'finance'), 'Process')
    $finance = Start-IntegratedChild 'finance' $pythonExe @('"' + $financeEntry + '"') $financeDir $financeEntry
    $started += $finance
    [Environment]::SetEnvironmentVariable('DATA_DIR', $effective.DATA_DIR, 'Process')
    Wait-IntegratedHTTP 'http://127.0.0.1:8765/finance/healthz' $finance.process
    if ($probeEnabled) {
        $probe = Start-IntegratedChild 'probe' $probeExe @() $probeDir $probeExe
        $started += $probe
        Wait-IntegratedHTTP 'http://127.0.0.1:18081/_siteadmin_probe/healthz' $probe.process
    }
    if (-not $WithoutFrontend) {
        $frontend = Start-IntegratedChild 'frontend' $nodeExe @('"' + $viteEntry + '"', '--host',
            '127.0.0.1', '--port', '5173', '--strictPort') $frontendDir $viteEntry
        $started += $frontend
        Wait-IntegratedHTTP 'http://127.0.0.1:5173/api/meta' $frontend.process
    }
    $url = if ($WithoutFrontend) { 'http://127.0.0.1:18080/' } else { 'http://127.0.0.1:5173/' }
    $session = @{
        mode = 'live'; url = $url; probe_started = [bool]$probeEnabled
        started_at = (Get-Date).ToUniversalTime().ToString('o')
        processes = @($started | ForEach-Object {
            @{ id = $_.process.Id; name = $_.name; executable = $_.executable; entry = $_.entry;
               start_time = $_.process.StartTime.ToUniversalTime().ToString('o') }
        })
    }
    Write-IntegratedPrivateJSON $sessionPath $session
    Write-Host "Integrated services ready: $url"
    Write-Host 'Finance shares the main source and site-admin authentication.'
    Write-Host "Generated machine credentials: $secretPath (not printed)."
    if (-not $probeEnabled) { Write-Host 'Login probe is not started; configure NEWAPI_UPSTREAM and use -EnableProbe.' }
    Write-Host 'Source readiness and external notification delivery must be checked separately in /system and finance settings.'
} catch {
    foreach ($item in $started) {
        $item.process.Refresh()
        if (-not $item.process.HasExited) { Stop-Process -Id $item.process.Id -ErrorAction SilentlyContinue }
    }
    throw
} finally {
    foreach ($key in $previousEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($key, $previousEnvironment[$key], 'Process')
    }
}
