# Photo Sorter

Sorts camera files into `<destination>\yyyymm\JPG|RAW|MOV`, by the date each photo or video was taken.
Nothing is copied or moved until you have seen the dry-run preview and clicked **Approve**.

> **Built by AI.** The code, tests and docs in this repository were written by Claude (Anthropic's AI model), directed and tested by [@tim80732](https://github.com/tim80732). It's used on real photo libraries, but review it yourself before trusting it with files you can't replace, and keep backups.

One program, two ways to run it:

| | Windows | Unraid / Docker |
|---|---|---|
| What | `PhotoSorter.exe` (≈8 MB, nothing to install) | Image `ghcr.io/tim80732/photo-sorter`, WebUI on port 8095 |
| Window | Its own app window (WebView2, built into Windows 10 and 11) | Browser: Docker tab → photo-sorter → WebUI |
| Settings | `%APPDATA%\PhotoSorter` | `/mnt/user/appdata/photo-sorter/config` |

## Install

**Windows:** download `PhotoSorter.exe` from the [latest release](https://github.com/tim80732/photo-sorter/releases/latest) and run it. It isn't code-signed, so SmartScreen may ask you to confirm the first time.

**Unraid:**

1. In a terminal on the server, download the template:

   ```bash
   wget -O /boot/config/plugins/dockerMan/templates-user/my-photo-sorter.xml https://raw.githubusercontent.com/tim80732/photo-sorter/main/docker/photo-sorter.xml
   ```

2. Docker tab → **Add Container** → pick **photo-sorter** from the template list.
3. Set **Camera photos** to the share that holds both your to-sort folders and your archive.
4. Apply, then open the WebUI.

The template is kept on the flash drive, so it survives reboots. Updates show up on the Docker tab like any other container.

**Any Docker host:**

```bash
docker run -d --name photo-sorter --user 99:100 -p 8095:8095 \
  -v /path/to/photos:/camera-photo -v /path/to/config:/config \
  ghcr.io/tim80732/photo-sorter:latest
```

The web UI has no login: keep it on your LAN and don't forward the port.

## How to use it

1. **New job:** pick source folders (an SD card, or a to-sort folder on the server) and a destination. Or use a **profile**, which remembers both plus your settings.
2. **Preview:** a dry run lists every folder and file: what will be copied, duplicates already in the archive, names that clash, and files without a capture date. Nothing is written yet.
3. **Approve:** files are copied (or moved) and each one is verified. Jobs run in the background; **Active jobs** shows them, and a stopped job can be continued.
4. **Job history** keeps what happened to every file. **Leftovers** shows what's still in a to-sort folder, and which job left it there.

## How it sorts

- **Folders:** `yyyymm` from the capture date. Every month folder gets `JPG`, `RAW` and `MOV`, even if some stay empty. An existing folder in another case (e.g. an old `raw`) is reused, not doubled.
- **Types:**
  - JPG/JPEG/HEIC/HIF → `JPG`.
  - All common RAW formats → `RAW`: CR3 CR2 CRW NEF NRW RAF ARW SRF SR2 DNG ORF RW2 RAW RWL PEF 3FR FFF IIQ X3F SRW ERF MEF MOS KDC DCR MRW GPR.
  - MP4/MOV/AVI/MTS/M2TS/M4V/3GP/CRM/MXF → `MOV`.
- **Capture date:** EXIF DateTimeOriginal for photos (read directly, no ExifTool). Videos are tried in this order:
  1. the Canon clip name (`A_0001C001A250130_125725…` = 2025-01-30 12:57:25)
  2. the camera's own metadata (Canon, Fujifilm MVTG, Nikon, Apple)
  3. the video header

  Files with no metadata fall back to a date in the file name, then the file's modified time. The preview lists every fallback.
- **Names:** with *Add the capture date-time* on, `DSCF0001.RAF` becomes `DSCF0001-20260328-164549.RAF`. Names that already contain their date are left alone.
- **Duplicates:** a file whose identical copy (same size, then SHA-256) is already in that month folder is skipped and left where it is. A *different* file with a taken name gets `_1`, `_2`… Nothing is ever overwritten.
- **Copy:** each file is written to a hidden temp file, saved to the disk, and read back from the disk itself: both Windows and Linux read it with the system's file cache bypassed. Only when the checksum matches is it renamed into place, so a drive that stored the data wrongly is caught too. Created and modified times are kept.
  - *Quick check* (a per-profile setting) skips the disk round trip and compares the copy as the system still holds it in memory. It's faster, and catches read and copy errors but not a bad write.
  - On an Unraid user share (`/mnt/user`) the read-back reaches the pool or array disk underneath. There the share layer is the slowest part either way, so the disk check costs about the same as the quick check. On a network share opened from Windows, the server may still answer the read-back from its own memory.
- **Move:** on the same disk it's a rename (instant, data untouched). From another disk it's copy → verify → delete the original. Moves force the copy onto the disk (`fsync`) before the original is deleted.
- **Speed:**
  - Several files are copied at once, and each copy reads ahead while it writes.
  - *Auto* picks 4 at once for SSDs, SD cards and network drives, and 1 when a spinning hard disk is involved. With the disk check on and only local flash involved it picks 8, because each file waits for its read-back. On Unraid it picks 2, because of the parity array. Profiles can override this.
  - Copies are checked with a fast 64-bit checksum, worked out while reading the original, so only the read-back costs time. SHA-256 is only used to decide that two files are identical.
  - The Windows app shows progress on its taskbar icon, like Explorer: green, or red if a file failed. The icon flashes when a sort finishes in the background.
  - Benchmark: `.\test\bench.ps1 -Src <folder>` compares Photo Sorter with Windows' copy and robocopy.
- **Sidecars:** `.xmp`, `.pp3` and `.dop` files go next to their photo, preferring the RAW, and are renamed to match.
- **Skipped and reported:**
  - camera/system files: `.CTG .THM .LRV .XML`, AVCHD index files, `.DS_Store`, `._*`
  - unknown types
- **Leftovers page:** what's still in a profile's to-sort folder after sorting (duplicates, system files and so on), so you can delete it. Empty card folders (`DCIM\100_FUJI`) are removed after a Move; the profile's own folder is always kept.

## Build from source

Needs Go (`winget install GoLang.Go`). From this folder in PowerShell:

```powershell
.\build.ps1                 # -> dist\PhotoSorter.exe and dist\linux\photosorter
.\test\e2e.ps1 -Samples <folder with a few real camera files>   # 42 end-to-end checks
.\deploy-unraid.ps1 -Ssh <ssh alias> -HostIp <server IP>         # dev only: build the image on an Unraid server
```

`build.ps1 -Version 1.1.0` sets the version shown in the app.

Releases are built by GitHub Actions: pushing a tag like `v1.0.0` builds `PhotoSorter.exe` for the release page and pushes the Docker image to `ghcr.io/tim80732/photo-sorter` (`:1.0.0` and `:latest`).

## License

MIT, see [LICENSE](LICENSE).

## Layout of the code

| Path | What |
|---|---|
| `internal/meta` | Capture-date readers: TIFF/EXIF, JPEG, ISO-BMFF (CR3, HEIC, MP4/MOV), RAF, CRW, MRW, MTS, AVI |
| `internal/sorter` | Scan → plan (dry run) → run, duplicate checks, leftovers |
| `internal/store` | Profiles, settings, history (JSON files) |
| `internal/server` | Web UI (`static/`) and JSON API |
| `main_windows.go` | WebView2 window, Windows folder dialog |
| `docker/` | Dockerfile (`FROM scratch`) and the Unraid template |

Command line, mostly for testing:
- `PhotoSorter.exe plan --src X --dest Y [--move] [--run] --out report.json`
- `PhotoSorter.exe meta <files>`
