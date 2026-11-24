[CmdletBinding()]
param(
    [string]$DataDirectory = '',
    [string]$Proxy = '',
    [switch]$SkipGeo
)

$ErrorActionPreference = 'Stop'
$projectPath = [IO.Path]::GetFullPath($PSScriptRoot)
if (-not $DataDirectory) { $DataDirectory = Join-Path $projectPath 'data\ip-intelligence' }
$dataPath = [IO.Path]::GetFullPath($DataDirectory)
$version = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
$generationPath = Join-Path $dataPath "versions\$version"
New-Item -ItemType Directory -Path $generationPath -Force | Out-Null

$datasets = @(
    @{ Name = 'vpn-ipv4.txt'; Kind = 'prefix'; URLs = @('https://raw.githubusercontent.com/X4BNet/lists_vpn/main/output/vpn/ipv4.txt'); Limit = 16777216 },
    @{ Name = 'vpn-ipv6.txt'; Kind = 'prefix'; URLs = @('https://raw.githubusercontent.com/X4BNet/lists_vpn/main/output/vpn/ipv6.txt'); Limit = 16777216 },
    @{ Name = 'non-residential-ipv4.txt'; Kind = 'prefix'; URLs = @('https://raw.githubusercontent.com/X4BNet/lists_vpn/main/output/datacenter/ipv4.txt'); Limit = 33554432 },
    @{ Name = 'non-residential-ipv6.txt'; Kind = 'prefix'; URLs = @('https://raw.githubusercontent.com/X4BNet/lists_vpn/main/output/datacenter/ipv6.txt'); Limit = 33554432 },
    @{ Name = 'tor-exits.txt'; Kind = 'address'; URLs = @('https://check.torproject.org/torbulkexitlist'); Limit = 8388608 },
    @{ Name = 'X4BNet-README.md'; Kind = 'notice'; URLs = @('https://raw.githubusercontent.com/X4BNet/lists_vpn/main/README.md'); Limit = 1048576 },
    @{ Name = 'GeoLite-README.md'; Kind = 'notice'; URLs = @('https://raw.githubusercontent.com/P3TERX/GeoLite.mmdb/master/README.md', 'https://raw.githubusercontent.com/P3TERX/GeoLite.mmdb/main/README.md'); Limit = 1048576 }
)
if (-not $SkipGeo) {
    $datasets += @(
        @{ Name = 'GeoLite2-City.mmdb'; Kind = 'mmdb'; URLs = @('https://github.com/P3TERX/GeoLite.mmdb/releases/latest/download/GeoLite2-City.mmdb', 'https://raw.githubusercontent.com/adysec/IP_database/main/geolite/GeoLite2-City.mmdb'); Limit = 167772160 },
        @{ Name = 'GeoLite2-ASN.mmdb'; Kind = 'mmdb'; URLs = @('https://github.com/P3TERX/GeoLite.mmdb/releases/latest/download/GeoLite2-ASN.mmdb', 'https://raw.githubusercontent.com/adysec/IP_database/main/geolite/GeoLite2-ASN.mmdb'); Limit = 67108864 }
    )
}

function Assert-DownloadedData([string]$filePath, [string]$kind) {
    if ($kind -eq 'mmdb') {
        $stream = [IO.File]::OpenRead($filePath)
        try {
            $length = [Math]::Min([long]131072, $stream.Length)
            [void]$stream.Seek(-$length, [IO.SeekOrigin]::End)
            $bytes = [byte[]]::new([int]$length)
            [void]$stream.Read($bytes, 0, $bytes.Length)
            if ([Text.Encoding]::ASCII.GetString($bytes).IndexOf('MaxMind.com') -lt 0) {
                throw 'The downloaded file is not an MMDB database.'
            }
        } finally { $stream.Dispose() }
        return
    }
    if ($kind -eq 'notice') { return }
    $records = 0
    foreach ($line in [IO.File]::ReadLines($filePath)) {
        $value = $line.Trim()
        if (-not $value -or $value.StartsWith('#')) { continue }
        $parts = $value.Split('/')
        $address = $null
        if ($parts.Length -gt 2 -or -not [Net.IPAddress]::TryParse($parts[0], [ref]$address)) {
            throw 'The downloaded list contains an invalid IP address.'
        }
        if ($kind -eq 'prefix') {
            $prefix = 0
            $bits = if ($address.AddressFamily -eq [Net.Sockets.AddressFamily]::InterNetwork) { 32 } else { 128 }
            if ($parts.Length -ne 2 -or -not [int]::TryParse($parts[1], [ref]$prefix) -or $prefix -lt 1 -or $prefix -gt $bits) {
                throw 'The downloaded list contains an invalid network prefix.'
            }
        } elseif ($parts.Length -ne 1) { throw 'The Tor list contains a network instead of an exit address.' }
        $records++
    }
    if ($records -eq 0) { throw 'The downloaded IP list is empty.' }
}

$downloadHandler = [Net.Http.HttpClientHandler]::new()
$downloadHandler.AllowAutoRedirect = $true
if ($Proxy) {
    $downloadHandler.Proxy = [Net.WebProxy]::new([Uri]$Proxy)
    $downloadHandler.UseProxy = $true
}
$downloadClient = [Net.Http.HttpClient]::new($downloadHandler)
$downloadClient.Timeout = [TimeSpan]::FromSeconds(180)
$downloadClient.DefaultRequestHeaders.UserAgent.ParseAdd('SiteAdmin-IPData/1.0')
$manifestFiles = @()
try {
    foreach ($dataset in $datasets) {
        $targetPath = Join-Path $generationPath $dataset.Name
        $downloaded = $false
        foreach ($url in $dataset.URLs) {
            $response = $null
            $inputStream = $null
            $outputStream = $null
            try {
                Write-Host "Downloading $($dataset.Name)"
                $response = $downloadClient.GetAsync($url, [Net.Http.HttpCompletionOption]::ResponseHeadersRead).GetAwaiter().GetResult()
                [void]$response.EnsureSuccessStatusCode()
                if ($response.Content.Headers.ContentLength -gt $dataset.Limit) { throw 'Download exceeds its size limit.' }
                $inputStream = $response.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
                $outputStream = [IO.File]::Create($targetPath)
                $buffer = [byte[]]::new(65536)
                $written = [long]0
                while (($received = $inputStream.Read($buffer, 0, $buffer.Length)) -gt 0) {
                    $written += $received
                    if ($written -gt $dataset.Limit) { throw 'Download exceeds its size limit.' }
                    $outputStream.Write($buffer, 0, $received)
                }
                $outputStream.Dispose()
                $outputStream = $null
                Assert-DownloadedData $targetPath $dataset.Kind
                $manifestFiles += @{
                    name = $dataset.Name
                    source = $url
                    resolved_source = $response.RequestMessage.RequestUri.GetLeftPart([UriPartial]::Path)
                    bytes = $written
                    sha256 = (Get-FileHash -LiteralPath $targetPath -Algorithm SHA256).Hash.ToLowerInvariant()
                    retrieved_at = (Get-Date).ToUniversalTime().ToString('o')
                }
                $downloaded = $true
                break
            } catch {
                Write-Warning "$($dataset.Name): $($_.Exception.Message)"
            } finally {
                if ($outputStream) { $outputStream.Dispose() }
                if ($inputStream) { $inputStream.Dispose() }
                if ($response) { $response.Dispose() }
            }
        }
        if (-not $downloaded) { throw "Could not install $($dataset.Name). The active dataset was not changed." }
    }
} finally { $downloadClient.Dispose() }

$noticeText = @'
IP intelligence data sources

X4BNet/lists_vpn:
https://github.com/X4BNet/lists_vpn
The complete MIT license is preserved in the README in this directory. The source
README documents the IPLocate ASN upstream and its CC BY-SA 4.0 attribution.
The datacenter collection includes VPNs and datacenter networks; it is not
proof of VPN usage. Missing list membership is not a negative classification.

GeoLite2:
Database content originates with MaxMind and is mirrored by P3TERX/GeoLite.mmdb.
The mirror README and the database's embedded metadata retain provenance.
https://www.maxmind.com/en/geolite2/eula
https://creativecommons.org/licenses/by-sa/4.0/

Tor:
https://check.torproject.org/torbulkexitlist
Current published exit addresses; a non-match does not prove the absence of Tor.
No user IPs are submitted when downloading these shared offline datasets.
'@
[IO.File]::WriteAllText((Join-Path $generationPath 'DATA-SOURCES.txt'), $noticeText, [Text.UTF8Encoding]::new($false))
$manifest = @{
    version = $version
    created_at = (Get-Date).ToUniversalTime().ToString('o')
    files = $manifestFiles
}
[IO.File]::WriteAllText((Join-Path $generationPath 'manifest.json'), ($manifest | ConvertTo-Json -Depth 6), [Text.UTF8Encoding]::new($false))
$activeFile = Join-Path $dataPath 'active.json'
$activeTemporary = Join-Path $dataPath "active-$version.tmp"
$active = @{ version = $version; directory = "versions/$version"; installed_at = (Get-Date).ToUniversalTime().ToString('o') }
[IO.File]::WriteAllText($activeTemporary, ($active | ConvertTo-Json), [Text.UTF8Encoding]::new($false))
[IO.File]::Move($activeTemporary, $activeFile, $true)
Write-Host "IP intelligence installed: $generationPath"
Write-Host 'Restart the site-admin service to load this generation.'
