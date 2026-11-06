# Changelog

What changed in each release. Downloads are on the
[Releases page](https://github.com/tim80732/photo-sorter/releases); the Docker
image is `ghcr.io/tim80732/photo-sorter`.

## 1.4.0 — 2026-11-06

### Added
- **File name pattern.** A *File name* box on the Import and Rename pages
  sets the new names: any text plus `{Name}`, `{Date}` and `{Time}`, added
  with buttons, with an example as you type. Leave it empty to keep names.
  The preview lists every name that changes, old and new. Profiles keep their
  own default (replacing the *Add the capture date-time* switch), which can
  be changed for one import; a manual import and Rename remember the last one
  used. `photosorter plan --pattern` does the same from the command line.

### Changed
- A name is kept only when it already fits the pattern. A date that's part
  of a camera's own name (`IMG_20250101_081151.HEIC`, Canon clips) no longer
  stops a rename.

### Fixed
- Rename jobs said *Moving* and *moved*; they now say *Renaming* and *renamed*.
- Rename preview trees count the files in subfolders too, so a parent folder
  no longer shows 0.

## 1.3.1 — 2026-11-06

### Changed
- The progress bar shows how a sort ended: green when done, amber when it
  finished with problems, red when it failed. A cancelled sort stays grey.

## 1.3.0 — 2026-11-06

### Added
- **Rename** tab: tidies a folder that's already an archive, in place. Pick
  the parent folder and the file types to sort; everything under it goes into
  `yyyymm/JPG | RAW | MOV` with the capture date-time in its name. Stray
  subfolders are emptied and removed, photos in the wrong month move to the
  right one, unticked types go to `<parent>/RAW` or `<parent>/MOV`, and other
  files (and exact duplicates) go to `<parent>/misc`. The preview shows the
  folder tree before and after, with file counts.

### Changed
- *New job* is now called **Import**.

## 1.2.0 — 2026-11-04

### Added
- **Free space in the preview.** A *Space* row shows how much the sort needs
  and how much is free on the destination, and warns when under 20 GB would
  be left. A move within the same disk needs no space. If the files don't
  fit, the preview says *Not enough space* and only offers *Back to
  settings*; the sort can't be approved.
- **Log for the Docker app** (`docker logs photo-sorter`): each scan and sort,
  every file a sort failed on, every file deleted from the Leftovers page,
  and sorts refused for lack of space.

### Changed
- The web server drops connections that never finish sending a request
  (10 s to send the request headers, idle connections closed after 2 min).

## 1.1.0 — 2026-11-03

### Changed
- **Copies are checked against the disk by default.** Each copy is saved,
  then read back from the disk itself (bypassing the system's file cache) and
  compared with the original before it counts. This also catches a drive that
  stored the data wrongly. It works on Unraid user shares too.
- *Quick check* (a per-profile setting) keeps the old, faster check against
  the copy still in memory.
- *Auto* copies 8 files at once when the disk check is on and only SSDs or
  flash are involved.

### Added
- The sorting page shows whether each file is being copied or checked.
- The summary, profile line and Job history say which check was used.

### Fixed
- Folder paths on the Unraid page showed Windows-style `\`.
- A stray "null" next to *Close* at the end of a sort in the Docker app.
- Windows app without WebView2 (browser mode): *Quit* now lets the current
  file finish instead of stopping mid-copy.
- `photosorter meta` now reports files it couldn't read instead of skipping
  them silently.
- The exe's file details now show a company and copyright.

## 1.0.0 — 2026-10-31

First release: sorts camera files into `yyyymm/JPG|RAW|MOV` by capture date,
with a dry-run preview to approve. Windows app and Docker image for Unraid.
