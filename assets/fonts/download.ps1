# Downloads the overlay TTFs into this folder. Idempotent: already-present
# files are skipped. Run from the repo root:
#   .\assets\fonts\download.ps1
#
# Sources are static-weight TTFs from the fontsource CDN (jsdelivr). We
# intentionally avoid google/fonts because that repo now only ships variable
# fonts at the top level and FFmpeg's drawtext can't select an instance
# from a variable font without extra config.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

try {
    [System.Net.ServicePointManager]::SecurityProtocol = `
        [System.Net.ServicePointManager]::SecurityProtocol -bor [System.Net.SecurityProtocolType]::Tls12
} catch {}

$dest = $PSScriptRoot
if (-not (Test-Path $dest)) { New-Item -ItemType Directory -Path $dest -Force | Out-Null }

$base = 'https://cdn.jsdelivr.net/fontsource/fonts'
$files = @(
    @{ name = 'Inter-Regular.ttf';           url = "$base/inter@latest/latin-400-normal.ttf" },
    @{ name = 'Inter-Bold.ttf';              url = "$base/inter@latest/latin-700-normal.ttf" },
    @{ name = 'Inter-Italic.ttf';            url = "$base/inter@latest/latin-400-italic.ttf" },
    @{ name = 'Inter-BoldItalic.ttf';        url = "$base/inter@latest/latin-700-italic.ttf" },
    @{ name = 'Roboto-Regular.ttf';          url = "$base/roboto@latest/latin-400-normal.ttf" },
    @{ name = 'Roboto-Bold.ttf';             url = "$base/roboto@latest/latin-700-normal.ttf" },
    @{ name = 'Roboto-Italic.ttf';           url = "$base/roboto@latest/latin-400-italic.ttf" },
    @{ name = 'Roboto-BoldItalic.ttf';       url = "$base/roboto@latest/latin-700-italic.ttf" },
    @{ name = 'Montserrat-Regular.ttf';      url = "$base/montserrat@latest/latin-400-normal.ttf" },
    @{ name = 'Montserrat-Bold.ttf';         url = "$base/montserrat@latest/latin-700-normal.ttf" },
    @{ name = 'PlayfairDisplay-Regular.ttf'; url = "$base/playfair-display@latest/latin-400-normal.ttf" },
    @{ name = 'PlayfairDisplay-Bold.ttf';    url = "$base/playfair-display@latest/latin-700-normal.ttf" },
    @{ name = 'BebasNeue-Regular.ttf';       url = "$base/bebas-neue@latest/latin-400-normal.ttf" }
)

$ok = 0
$skip = 0
$fail = 0

foreach ($f in $files) {
    $out = Join-Path $dest $f.name
    if (Test-Path $out) {
        Write-Host ("  [skip] " + $f.name) -ForegroundColor DarkGray
        $skip = $skip + 1
        continue
    }
    try {
        Write-Host ("  [get ] " + $f.name) -ForegroundColor Cyan
        Invoke-WebRequest -Uri $f.url -OutFile $out -UseBasicParsing
        $ok = $ok + 1
    }
    catch {
        $msg = $_.Exception.Message
        Write-Host ("  [FAIL] " + $f.name + " -- " + $msg) -ForegroundColor Red
        $fail = $fail + 1
    }
}

Write-Host ""
$summary = "Done: " + $ok + " downloaded, " + $skip + " already present, " + $fail + " failed."
Write-Host $summary -ForegroundColor Green
if ($fail -gt 0) { exit 1 }
