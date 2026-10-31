package meta

import (
	"bytes"
	"encoding/binary"
	"strings"
	"time"
)

// EXIF tags we care about.
const (
	tagDateTime          = 0x0132
	tagDateTimeOriginal  = 0x9003
	tagDateTimeDigitized = 0x9004
	tagExifIFD           = 0x8769
	tagRW2JpgFromRaw     = 0x002e
)

// readTIFF parses a TIFF/EXIF structure starting at base and returns the best
// capture date: DateTimeOriginal, then DateTimeDigitized, then DateTime.
// It accepts the non-standard magic numbers used by ORF ("RO"/"RS") and
// RW2 (0x55), since only the IFD layout matters here.
func readTIFF(r *rd, base int64) (time.Time, bool) {
	h := r.bytes(base, 8)
	if h == nil {
		return time.Time{}, false
	}
	var bo binary.ByteOrder
	switch string(h[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return time.Time{}, false
	}
	return ifdDates(r, bo, base, bo.Uint32(h[4:8]))
}

// ifdDates reads the date tags from the IFD at base+ifd0 and its Exif sub-IFD.
// Offsets inside the IFDs are relative to base.
func ifdDates(r *rd, bo binary.ByteOrder, base int64, ifd0 uint32) (time.Time, bool) {
	tags := map[uint16]string{}
	var exifPtr, jpgOff, jpgLen uint32
	visit := func(ifd uint32) {
		readIFD(r, bo, base, ifd, func(tag, typ uint16, count, val uint32, inline []byte) {
			switch tag {
			case tagDateTime, tagDateTimeOriginal, tagDateTimeDigitized:
				if typ == 2 && count >= 19 {
					var s []byte
					if count <= 4 {
						s = inline
					} else {
						s = r.bytes(base+int64(val), int(count))
					}
					if s != nil {
						if _, seen := tags[tag]; !seen {
							tags[tag] = string(s)
						}
					}
				}
			case tagExifIFD:
				exifPtr = val
			case tagRW2JpgFromRaw:
				jpgOff, jpgLen = val, count
			}
		})
	}
	visit(ifd0)
	if exifPtr != 0 {
		visit(exifPtr)
	}
	for _, t := range []uint16{tagDateTimeOriginal, tagDateTimeDigitized, tagDateTime} {
		if s, ok := tags[t]; ok {
			if tm, ok := parseExifDate(s); ok {
				return tm, true
			}
		}
	}
	if jpgOff != 0 && jpgLen > 0 { // Panasonic RW2 keeps full EXIF in an embedded JPEG
		return readJPEG(r, base+int64(jpgOff))
	}
	return time.Time{}, false
}

func readIFD(r *rd, bo binary.ByteOrder, base int64, off uint32, fn func(tag, typ uint16, count, val uint32, inline []byte)) {
	nb := r.bytes(base+int64(off), 2)
	if nb == nil {
		return
	}
	n := int(bo.Uint16(nb))
	if n == 0 || n > 1000 {
		return
	}
	ents := r.bytes(base+int64(off)+2, n*12)
	if ents == nil {
		return
	}
	for i := 0; i < n; i++ {
		e := ents[i*12 : i*12+12]
		fn(bo.Uint16(e[0:2]), bo.Uint16(e[2:4]), bo.Uint32(e[4:8]), bo.Uint32(e[8:12]), e[8:12])
	}
}

// parseExifDate parses "YYYY:MM:DD HH:MM:SS" (also tolerating '-' or 'T'
// separators) as a wall-clock time. Zeroed or out-of-range dates fail.
func parseExifDate(s string) (time.Time, bool) {
	s = strings.TrimRight(s, "\x00 ")
	if len(s) < 19 {
		return time.Time{}, false
	}
	b := []byte(s[:19])
	for _, i := range []int{4, 7} {
		if b[i] == '-' || b[i] == '/' {
			b[i] = ':'
		}
	}
	if b[10] == 'T' {
		b[10] = ' '
	}
	t, err := time.Parse("2006:01:02 15:04:05", string(b))
	if err != nil {
		return time.Time{}, false
	}
	return t, plausible(t)
}

func plausible(t time.Time) bool {
	return t.Year() >= 1980 && t.Before(time.Now().AddDate(1, 0, 0))
}

// readJPEG walks JPEG segments from off looking for an APP1 EXIF block.
func readJPEG(r *rd, off int64) (time.Time, bool) {
	if h := r.bytes(off, 2); h == nil || h[0] != 0xFF || h[1] != 0xD8 {
		return time.Time{}, false
	}
	p := off + 2
	for i := 0; i < 64; i++ {
		h := r.bytes(p, 4)
		if h == nil || h[0] != 0xFF {
			return time.Time{}, false
		}
		if h[1] == 0xFF { // fill byte
			p++
			continue
		}
		m := h[1]
		if m == 0xDA || m == 0xD9 { // start of scan / end of image
			break
		}
		l := int64(binary.BigEndian.Uint16(h[2:4]))
		if m == 0xE1 {
			if id := r.bytes(p+4, 6); id != nil && bytes.Equal(id, []byte("Exif\x00\x00")) {
				if t, ok := readTIFF(r, p+10); ok {
					return t, true
				}
			}
		}
		p += 2 + l
	}
	return time.Time{}, false
}

// scanForExif looks through the first MB for an embedded EXIF block, for
// formats we don't parse structurally (X3F, odd RAW variants).
func scanForExif(r *rd) (time.Time, bool) {
	n := int64(1 << 20)
	if n > r.size {
		n = r.size
	}
	buf := r.bytes(0, int(n))
	if buf == nil {
		return time.Time{}, false
	}
	for _, sig := range [][]byte{[]byte("Exif\x00\x00II*\x00"), []byte("Exif\x00\x00MM\x00*")} {
		if i := bytes.Index(buf, sig); i >= 0 {
			if t, ok := readTIFF(r, int64(i)+6); ok {
				return t, true
			}
		}
	}
	return time.Time{}, false
}
