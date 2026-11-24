[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$projectDir = [IO.Path]::GetFullPath($PSScriptRoot)
$runtimeDir = Join-Path $projectDir 'output\integrated-runtime'
$sessionPath = Join-Path $runtimeDir 'session.json'
if (-not (Test-Path -LiteralPath $sessionPath -PathType Leaf)) {
    Write-Host 'No integrated session is recorded.'
    exit 0
}
$allowedEntries = @(
    (Join-Path $runtimeDir 'siteadmin-integrated.exe'),
    (Join-Path $runtimeDir 'login-probe.exe'),
    (Join-Path $projectDir 'finance\run.py'),
    (Join-Path $projectDir 'frontend\node_modules\vite\bin\vite.js')
)
$session = Get-Content -LiteralPath $sessionPath -Raw | ConvertFrom-Json
$unmatched = 0
foreach ($item in @($session.processes)) {
    if ([string]$item.entry -notin $allowedEntries) { $unmatched++; continue }
    $process = Get-Process -Id ([int]$item.id) -ErrorAction SilentlyContinue
    if (-not $process) { continue }
    $owner = Get-CimInstance Win32_Process -Filter "ProcessId = $([int]$item.id)" -ErrorAction SilentlyContinue
    $line = if ($owner) { ([string]$owner.CommandLine).Replace('/', '\') } else { '' }
    $start = ([DateTime]::Parse([string]$item.start_time)).ToUniversalTime()
    $sameStart = [Math]::Abs(($process.StartTime.ToUniversalTime() - $start).TotalSeconds) -lt 1
    if ($sameStart -and $process.Path -eq [string]$item.executable -and
        $line.Contains(([string]$item.entry).Replace('/', '\'))) {
        Stop-Process -Id $process.Id -ErrorAction Stop
        Write-Host "Stopped: $($item.name)"
    } else { $unmatched++ }
}
if ($unmatched) {
    Write-Host "Kept $unmatched unmatched process record(s); no unrelated process was stopped."
} else {
    Remove-Item -LiteralPath $sessionPath -Force
}
