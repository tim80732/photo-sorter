package meta

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Result is a capture date as a wall-clock time. The Location is always UTC
// and carries no meaning: 10:15 means 10:15 on the camera's clock.
type Result struct {
	Time     time.Time
	Source   string // where the date came from, shown in the preview
	Fallback bool   // true when no metadata was found (file name or modified time)
}

// Date sources.
const (
	SrcExif       = "EXIF"
	SrcCanonClip  = "Canon clip name"
	SrcVideoUTC   = "Video header (UTC)"
	SrcVideoLocal = "Video header (camera clock)"
	SrcStream     = "Video stream"
	SrcFileName   = "File name"
	SrcModified   = "File modified time"
)

var tiffRaw = map[string]bool{
	"cr2": true, "nef": true, "nrw": true, "arw": true, "srf": true, "sr2": true,
	"dng": true, "orf": true, "ori": true, "rw2": true, "raw": true, "rwl": true,
	"pef": true, "3fr": true, "fff": true, "iiq": true, "srw": true, "erf": true,
	"mef": true, "mos": true, "kdc": true, "dcr": true, "gpr": true, "tif": true, "tiff": true,
}

// Read returns the capture date of a photo or video. mtime is the file's
// modified time, used to disambiguate video header times and as last resort.
func Read(path string, mtime time.Time) Result {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	name := filepath.Base(path)

	if isVideo(ext) {
		if t, ok := canonClipName(name); ok {
			return Result{Time: t, Source: SrcCanonClip}
		}
	}

	r, err := openRd(path)
	if err == nil {
		defer r.Close()
		if res, ok := readByType(r, ext, mtime); ok {
			return res
		}
	}

	if t, ok := dateInName(name); ok {
		return Result{Time: t, Source: SrcFileName, Fallback: true}
	}
	return Result{Time: naive(mtime.In(time.Local)), Source: SrcModified, Fallback: true}
}

func isVideo(ext string) bool {
	switch ext {
	case "mp4", "mov", "m4v", "3gp", "crm", "avi", "mts", "m2ts", "mxf":
		return true
	}
	return false
}

func readByType(r *rd, ext string, mtime time.Time) (Result, bool) {
	exif := func(t time.Time, ok bool) (Result, bool) { return Result{Time: t, Source: SrcExif}, ok }
	switch {
	case ext == "jpg" || ext == "jpeg" || ext == "jpe":
		if t, ok := readJPEG(r, 0); ok {
			return exif(t, ok)
		}
	case ext == "heic" || ext == "heif" || ext == "hif" || ext == "avif":
		if t, ok := readHEIF(r); ok {
			return exif(t, ok)
		}
	case ext == "cr3":
		if t, ok := readCR3(r); ok {
			return exif(t, ok)
		}
	case ext == "raf":
		if t, ok := readRAF(r); ok {
			return exif(t, ok)
		}
	case ext == "crw":
		if t, ok := readCIFF(r); ok {
			return exif(t, ok)
		}
	case ext == "mrw":
		if t, ok := readMRW(r); ok {
			return exif(t, ok)
		}
	case tiffRaw[ext]:
		if t, ok := readTIFF(r, 0); ok {
			return exif(t, ok)
		}
	case ext == "mts" || ext == "m2ts":
		if t, ok := readMTS(r); ok {
			return Result{Time: t, Source: SrcStream}, true
		}
		return Result{}, false
	case ext == "avi":
		if t, ok := readAVI(r); ok {
			return Result{Time: t, Source: SrcStream}, true
		}
		return Result{}, false
	case isVideo(ext):
		vi := readQuickTime(r)
		if !vi.local.IsZero() && plausible(vi.local) {
			return Result{Time: vi.local, Source: vi.localSrc}, true
		}
		if !vi.mvhd.IsZero() && plausible(vi.mvhd) {
			t, src := pickMvhd(vi, mtime)
			return Result{Time: t, Source: src}, true
		}
		return Result{}, false
	}
	// Unknown layout or a structural miss: look for an EXIF block anywhere early.
	if t, ok := scanForExif(r); ok {
		return exif(t, ok)
	}
	return Result{}, false
}

// pickMvhd decides how to read the mvhd time. The spec says UTC, but many
// cameras write their local clock. The file's modified time (written by the
// camera in local time) tells us which reading is right; if it can't, we
// follow the spec.
func pickMvhd(vi videoInfo, mtime time.Time) (time.Time, string) {
	asUTC := naive(vi.mvhd.In(time.Local))
	asLocal := vi.mvhd
	if asUTC.Equal(asLocal) {
		return asUTC, SrcVideoUTC
	}
	mt := naive(mtime.In(time.Local))
	dist := func(c time.Time) time.Duration {
		a, b := absDur(mt.Sub(c)), absDur(mt.Sub(c.Add(vi.duration)))
		if b < a {
			return b
		}
		return a
	}
	if dl, du := dist(asLocal), dist(asUTC); dl < du && dl < 36*time.Hour {
		return asLocal, SrcVideoLocal
	}
	return asUTC, SrcVideoUTC
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func naive(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

// Canon clip names: A_0001C008A260326_150130EJ_CANON.MP4 = 2026-03-26 15:01:30.
var canonClipRe = regexp.MustCompile(`(?i)^[A-Z]_\d{4}C\d{3}[A-Z](\d{2})(\d{2})(\d{2})_(\d{2})(\d{2})(\d{2})`)

func canonClipName(name string) (time.Time, bool) {
	m := canonClipRe.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	return mkTime(2000+atoi(m[1]), atoi(m[2]), atoi(m[3]), atoi(m[4]), atoi(m[5]), atoi(m[6]))
}

// Generic names: IMG_20250101_081151, PXL_20250101_081151123, 20231001_180130_x,
// and this app's own suffix x-20261003-101500.
var nameDateRe = regexp.MustCompile(`(?:^|\D)((?:19|20)\d{2})(\d{2})(\d{2})[_\-T ]?(\d{2})(\d{2})(\d{2})`)

func dateInName(name string) (time.Time, bool) {
	m := nameDateRe.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	return mkTime(atoi(m[1]), atoi(m[2]), atoi(m[3]), atoi(m[4]), atoi(m[5]), atoi(m[6]))
}

func mkTime(y, mo, d, h, mi, s int) (time.Time, bool) {
	if mo < 1 || mo > 12 || d < 1 || d > 31 || h > 23 || mi > 59 || s > 59 {
		return time.Time{}, false
	}
	t := time.Date(y, time.Month(mo), d, h, mi, s, 0, time.UTC)
	if t.Day() != d {
		return time.Time{}, false
	}
	return t, plausible(t)
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }
