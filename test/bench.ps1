# Copy-speed benchmark: Photo Sorter vs Windows' own copy (Explorer's engine) and robocopy.
#   .\test\bench.ps1 -Src <folder> [-Exe dist\PhotoSorter.exe] [-Work <scratch folder>]
# Only reads -Src. Each run writes a fresh copy under -Work and deletes it afterwards.
# Note: a second read of the same source may come from the Windows file cache.
param(
    [Parameter(Mandatory)] [string]$Src,
    [string]$Exe = "$PSScriptRoot\..\dist\PhotoSorter.exe",
    [string]$Work = "$env:TEMP\photosorter-bench"
)
$ErrorActionPreference = "Stop"
$Exe = (Resolve-Path $Exe).Path
$bytes = (Get-ChildItem -Recurse -File $Src | Measure-Object Length -Sum).Sum
New-Item -ItemType Directory -Force $Work | Out-Null

function Time($name, [scriptblock]$run) {
    $dest = Join-Path $Work ("out-" + [guid]::NewGuid().ToString("N").Substring(0, 6))
    $sw = [Diagnostics.Stopwatch]::StartNew()
    & $run $dest
    $sec = $sw.Elapsed.TotalSeconds
    Remove-Item -Recurse -Force $dest -ErrorAction SilentlyContinue
    [pscustomobject]@{ Method = $name; Seconds = [math]::Round($sec, 1); 'MB/s' = [math]::Round($bytes / 1MB / $sec) }
}
function Sorter($dest, $parallel) {
    $p = Start-Process $Exe -ArgumentList 'plan', '--src', "`"$Src`"", '--dest', "`"$dest`"", '--run', '--parallel', $parallel, '--out', "`"$dest.json`"" -Wait -PassThru -NoNewWindow
    $r = Get-Content "$dest.json" -Raw | ConvertFrom-Json
    Remove-Item "$dest.json"
    if ($p.ExitCode -or $r.summary.failed) { throw "photo sorter run failed" }
}

"{0:N2} GB in {1} files" -f ($bytes / 1GB), (Get-ChildItem -Recurse -File $Src).Count
@(
    Time "Windows copy (Explorer engine), 1 at a time" { param($d) Copy-Item -Recurse $Src $d }
    Time "robocopy, 8 threads, no verify" { param($d) robocopy $Src $d /E /MT:8 /NJH /NJS /NFL /NDL /NP | Out-Null }
    Time "Photo Sorter, 1 at a time + verify" { param($d) Sorter $d 1 }
    Time "Photo Sorter, auto + verify" { param($d) Sorter $d 0 }
    Time "Photo Sorter, 8 at once + verify" { param($d) Sorter $d 8 }
) | Format-Table -AutoSize
