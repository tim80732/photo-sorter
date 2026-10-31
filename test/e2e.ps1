# End-to-end test of the sorting engine through the CLI (photosorter plan).
#   .\test\e2e.ps1 -Samples <folder of real camera files> [-Exe dist\PhotoSorter.exe]
# The samples folder is only read. Everything else happens in a temp folder.
param(
    [Parameter(Mandatory)] [string]$Samples,
    [string]$Exe = "$PSScriptRoot\..\dist\PhotoSorter.exe",
    [string]$Work = "$env:TEMP\photosorter-e2e"
)
$ErrorActionPreference = "Stop"
$Exe = (Resolve-Path $Exe).Path
$fails = 0
function Check($ok, $what) {
    if ($ok) { Write-Host "  ok    $what" -ForegroundColor Green }
    else { Write-Host "  FAIL  $what" -ForegroundColor Red; $script:fails++ }
}
function Plan([string[]]$srcs, $dest, [string[]]$extra) {
    $out = Join-Path $Work ("report-" + [guid]::NewGuid().ToString("N").Substring(0, 6) + ".json")
    $a = @("plan")
    foreach ($s in $srcs) { $a += "--src"; $a += $s }
    $a += "--dest"; $a += $dest; $a += "--out"; $a += $out
    if ($extra) { $a += $extra }
    $p = Start-Process -FilePath $Exe -ArgumentList ($a | ForEach-Object { '"' + $_ + '"' }) -Wait -PassThru -NoNewWindow
    if ($p.ExitCode -ne 0) { throw "photosorter plan failed ($($p.ExitCode))" }
    Get-Content $out -Raw -Encoding UTF8 | ConvertFrom-Json
}
function Item($r, $name) { $r.plan.items | Where-Object { ($_.rel -split '[\\/]')[-1] -eq $name } }

# ---- build the test sources ----
if (Test-Path $Work) { Remove-Item -Recurse -Force $Work }
$card = "$Work\card\DCIM\100_FUJI"; $card2 = "$Work\card2"; $dest = "$Work\archive"
New-Item -ItemType Directory -Force $card, "$Work\card\MISC", $card2, "$dest\202603\RAW", "$dest\202510\raw" | Out-Null
Get-ChildItem $Samples -File -Include *.RAF, *.JPG, *.MOV -Recurse | Where-Object Name -like 'DSCF*' | Copy-Item -Destination $card
Get-ChildItem $Samples -File | Where-Object { $_.Name -notlike 'DSCF*' -or $_.Extension -eq '.dng' } | Copy-Item -Destination $card2

# a JPG with no EXIF at all, dated by its file time
Add-Type -AssemblyName System.Drawing
$bmp = New-Object System.Drawing.Bitmap 64, 48
$bmp.Save("$card\NOEXIF01.JPG", [System.Drawing.Imaging.ImageFormat]::Jpeg); $bmp.Dispose()
(Get-Item "$card\NOEXIF01.JPG").LastWriteTime = [datetime]"2026-02-14 10:00:00"
# a different file with the same name and capture time -> name clash
$clashDir = New-Item -ItemType Directory -Force "$card2\DCIM\101_FUJI"
Copy-Item "$card\DSCF7970.JPG" "$clashDir\DSCF7970.JPG"
[IO.File]::AppendAllText("$clashDir\DSCF7970.JPG", "x")
# an identical copy in the second source -> duplicate within this import
Copy-Item "$card\DSCF9505.RAF" "$clashDir\DSCF9505.RAF"
# already in the archive under its old name -> duplicate (quick check: same time)
Copy-Item "$card\DSCF9509.RAF" "$dest\202603\RAW\DSCF9509.RAF"
# identical, but its modified time changed -> still a duplicate (full hash)
New-Item -ItemType Directory -Force "$dest\202603\JPG" | Out-Null
Copy-Item "$card\DSCF8018.JPG" "$dest\202603\JPG\OLD8018.JPG"
(Get-Item "$dest\202603\JPG\OLD8018.JPG").LastWriteTime = [datetime]"2027-01-01"
# same size, differs in the middle, different time -> not a duplicate
$b = [IO.File]::ReadAllBytes("$card\DSCF8624.JPG"); $b[[int]($b.Length / 2)] = $b[[int]($b.Length / 2)] -bxor 0xFF
[IO.File]::WriteAllBytes("$dest\202603\JPG\FAKE8624.JPG", $b)
# sidecars, junk and an unknown file
Set-Content "$card\DSCF9504.xmp" "<x:xmpmeta/>"
Set-Content "$card\DSCF9505.RAF.pp3" "[Version]"
Set-Content "$card\ORPHAN.xmp" "<x:xmpmeta/>"
Set-Content "$card\FUJI.CTG" "idx"; Set-Content "$card\._DSCF9504.RAF" "mac"; Set-Content "$Work\card\MISC\clip.XML" "<x/>"
Set-Content "$Work\card\notes.pdf" "%PDF"

# ---- 1. dry run ----
Write-Host "Dry run"
$before = (Get-ChildItem -Recurse $dest -File).Count
$r = Plan @("$Work\card", $card2) $dest
Check ((Get-ChildItem -Recurse $dest -File).Count -eq $before) "dry run writes nothing"
$cr3 = Item $r '101A2290.CR3'
Check ($cr3.dest -eq '202310/RAW/101A2290-20231001-180130.CR3') "CR3 -> 202310/RAW with date in name ($($cr3.dest))"
Check ((Item $r 'DSCF9504.RAF').dest -like '202603/RAW/DSCF9504-20260328-164549.RAF') "RAF from EXIF"
Check ((Item $r 'IMG_20250101_081151.HEIC').dest -eq '202501/JPG/IMG_20250101_081151.HEIC') "HEIC -> JPG, name already dated"
$clip = Item $r 'A_0001C001A250130_125725EJ_CANON.MP4'
Check ($clip.dateSrc -eq 'Canon clip name' -and $clip.dest -eq '202501/MOV/A_0001C001A250130_125725EJ_CANON.MP4') "Canon clip dated from its name"
Check ((Item $r 'DSCF3113.MOV').dateSrc -eq 'Fujifilm metadata') "Fuji MOV dated from MVTG"
$dng = Item $r 'DSCF8925-全景.dng'
Check ($dng.dest -like '202603/RAW/DSCF8925-全景-20260322-092345.dng') "DNG with a Chinese name"
$noexif = Item $r 'NOEXIF01.JPG'
Check ($noexif.fallback -and $noexif.dateSrc -eq 'File modified time' -and $noexif.dest -like '202602/JPG/*') "no-EXIF JPG uses file time and is flagged"
$clash = @($r.plan.items | Where-Object { $_.rel -like '*DSCF7970.JPG' })
Check (($clash | Where-Object clash).dest -eq '202603/JPG/DSCF7970-20260321-100900_1.JPG') "name clash gets _1"
$dupIn = @($r.plan.items | Where-Object { $_.rel -like '*DSCF9505.RAF' })
Check (($dupIn | Where-Object action -eq 'duplicate').match -like '202603/RAW/DSCF9505-*') "identical file twice in one import -> one copy"
Check ((Item $r 'DSCF9509.RAF').match -eq '202603/RAW/DSCF9509.RAF') "file already in archive under old name -> duplicate"
Check ((Item $r 'DSCF8018.JPG').match -eq '202603/JPG/OLD8018.JPG') "identical file with a different modified time -> duplicate"
Check ((Item $r 'DSCF8624.JPG').action -eq 'copy') "same size but different content -> copied, not a duplicate"
Check ((Item $r 'DSCF9504.xmp').dest -eq '202603/RAW/DSCF9504-20260328-164549.xmp') "xmp follows its RAW's new name"
Check ((Item $r 'DSCF9505.RAF.pp3').dest -like '202603/RAW/DSCF9505-*.RAF.pp3') "pp3 keeps <photo>.RAF.pp3 form"
Check ((Item $r 'ORPHAN.xmp').reason -eq 'sidecar') "orphan sidecar is left with a reason"
Check ((Item $r 'FUJI.CTG').reason -eq 'system' -and (Item $r '._DSCF9504.RAF').reason -eq 'system' -and (Item $r 'clip.XML').reason -eq 'system') "camera system files skipped"
Check ((Item $r 'notes.pdf').reason -eq 'unknown') "unknown file reported"
$m510 = $r.plan.tree | Where-Object month -eq '202510'
Check (($m510.cats | Where-Object cat -eq 'RAW').dir -eq 'raw') "existing lower-case raw folder is reused"

# ---- 2. copy ----
Write-Host "Copy"
$srcCount = (Get-ChildItem -Recurse "$Work\card", $card2 -File).Count
$r2 = Plan @("$Work\card", $card2) $dest @("--run")
Check ($r2.summary.failed -eq 0) "no failures ($($r2.summary.copied) copied)"
Check ((Get-ChildItem -Recurse "$Work\card", $card2 -File).Count -eq $srcCount) "copy leaves sources alone"
Check (Test-Path "$dest\202310\RAW\101A2290-20231001-180130.CR3") "CR3 is in place"
Check ((Get-FileHash "$dest\202310\RAW\101A2290-20231001-180130.CR3").Hash -eq (Get-FileHash "$card2\101A2290.CR3").Hash) "copy is byte-identical"
Check ((Get-Item "$dest\202310\RAW\101A2290-20231001-180130.CR3").LastWriteTime -eq (Get-Item "$card2\101A2290.CR3").LastWriteTime) "modified time preserved"
Check ((Get-Item "$dest\202310\RAW\101A2290-20231001-180130.CR3").CreationTime -eq (Get-Item "$card2\101A2290.CR3").CreationTime) "created time preserved"
foreach ($m in Get-ChildItem $dest -Directory) {
    $sub = (Get-ChildItem $m.FullName -Directory).Name | ForEach-Object { $_.ToUpper() } | Sort-Object
    Check (($sub -join ',') -eq 'JPG,MOV,RAW') "$($m.Name) has JPG, RAW, MOV"
}
Check (-not (Test-Path "$dest\202510\RAW") -or (Get-Item "$dest\202510\raw").Name -ceq 'raw') "no second RAW next to raw"
Check (-not (Get-ChildItem -Recurse $dest -Filter '.photosorter-*' -Force)) "no temp files left"

# ---- 3. same sources again: everything is a duplicate ----
Write-Host "Copy again"
$r3 = Plan @("$Work\card", $card2) $dest
Check (@($r3.plan.items | Where-Object action -eq 'copy').Count -eq 0) "second run finds nothing new"

# ---- 4. move (same disk -> rename) ----
Write-Host "Move"
$mv = "$Work\dumps\fuji x-t50"; $mdest = "$Work\archive2"
New-Item -ItemType Directory -Force "$mv\DCIM" | Out-Null
Copy-Item -Recurse "$Work\card\DCIM\100_FUJI" "$mv\DCIM\100_FUJI"
Copy-Item "$card\DSCF9509.RAF" "$mv\DCIM\100_FUJI\COPYOF09.RAF"   # will be a duplicate within the run
$r4 = Plan @($mv) $mdest @("--move", "--run")
Check ($r4.summary.failed -eq 0 -and $r4.summary.moved -gt 0) "moved $($r4.summary.moved), no failures"
$left = Get-ChildItem -Recurse $mv -File | ForEach-Object Name
Check (@($left | Where-Object { $_ -in 'COPYOF09.RAF','DSCF9509.RAF' }).Count -eq 1) "of two identical files one is moved, the other stays as a duplicate"
Check ($left -contains 'FUJI.CTG' -and $left -contains 'ORPHAN.xmp') "leftovers stay"
Check (-not ($left -like 'DSCF950[45].RAF')) "moved RAWs are gone from the source"
Check (Test-Path $mv) "the to-sort folder itself is kept"

# ---- 5. move, keep names ----
Write-Host "Keep names"
$r5 = Plan @($card2) "$Work\archive3" @("--no-rename")
Check ((Item $r5 '101A2290.CR3').dest -eq '202310/RAW/101A2290.CR3') "--no-rename keeps the camera name"

Write-Host ""
if ($fails) { Write-Host "$fails check(s) failed" -ForegroundColor Red; exit 1 }
Write-Host "All checks passed" -ForegroundColor Green
