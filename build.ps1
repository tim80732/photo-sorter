# Builds Photo Sorter.
#   .\build.ps1            -> dist\PhotoSorter.exe (Windows app) and dist\linux\photosorter (for Docker)
# Needs Go (winget install GoLang.Go). go-winres is installed on first run.
param([string]$Version = "1.0.0")
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
$env:Path = [Environment]::GetEnvironmentVariable("Path", "Machine") + ";" + [Environment]::GetEnvironmentVariable("Path", "User") + ";$env:USERPROFILE\go\bin"

if (-not (Get-Command go-winres -ErrorAction SilentlyContinue)) {
    go install github.com/tc-hib/go-winres@latest
    if (-not $?) { throw "couldn't install go-winres" }
}
if (-not (Test-Path winres\icon.png)) { go run ./tools/mkicon winres/icon.png }
Copy-Item winres\icon.png internal\server\static\icon.png -Force # also served for the Unraid Docker tab

# Icon, version info and the DPI-aware manifest go into the exe as a .syso
go-winres make --in winres\winres.json --out rsrc --file-version $Version --product-version $Version
if (-not $?) { throw "go-winres failed" }

$ld = "-s -w -X photosorter/internal/server.Version=$Version"
New-Item -ItemType Directory -Force dist\linux | Out-Null

$env:CGO_ENABLED = "0"
$env:GOOS = "windows"; $env:GOARCH = "amd64"
go build -trimpath -ldflags "$ld -H windowsgui" -o dist\PhotoSorter.exe .
if (-not $?) { throw "Windows build failed" }

$env:GOOS = "linux"; $env:GOARCH = "amd64"
go build -trimpath -ldflags $ld -o dist\linux\photosorter .
if (-not $?) { throw "Linux build failed" }
Remove-Item Env:GOOS, Env:GOARCH

Copy-Item docker\Dockerfile dist\linux\Dockerfile -Force
Get-ChildItem dist\PhotoSorter.exe, dist\linux\photosorter | ForEach-Object { "{0,-28} {1,8:N1} MB" -f $_.Name, ($_.Length / 1MB) }
