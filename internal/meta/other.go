package meta

import (
	"bytes"
	"encoding/binary"
	"strings"
	"time"
)

// readRAF: Fujifilm RAF has a JPEG preview (with full EXIF) at the offset
// stored at byte 84.
func readRAF(r *rd) (time.Time, bool) {
	if h := r.bytes(0, 15); h == nil || string(h) != "FUJIFILMCCD-RAW" {
		return time.Time{}, false
	}
	off, ok := r.be32(84)
	if !ok {
		return time.Time{}, false
	}
	return readJPEG(r, int64(off))
}

// readCIFF: old Canon CRW files use the CIFF heap format; tag 0x180e holds the
// capture time as local seconds since 1970.
func readCIFF(r *rd) (time.Time, bool) {
	h := r.bytes(0, 14)
	if h == nil || string(h[0:2]) != "II" || string(h[6:14]) != "HEAPCCDR" {
		return time.Time{}, false
	}
	hlen := int64(binary.LittleEndian.Uint32(h[2:6]))
	return ciffHeap(r, hlen, r.size, 0)
}

func ciffHeap(r *rd, start, end int64, depth int) (time.Time, bool) {
	if depth > 4 || end-start < 6 {
		return time.Time{}, false
	}
	tb := r.bytes(end-4, 4)
	if tb == nil {
		return time.Time{}, false
	}
	tbl := start + int64(binary.LittleEndian.Uint32(tb))
	nb := r.bytes(tbl, 2)
	if nb == nil {
		return time.Time{}, false
	}
	n := int(binary.LittleEndian.Uint16(nb))
	if n > 512 {
		return time.Time{}, false
	}
	ents := r.bytes(tbl+2, n*10)
	if ents == nil {
		return time.Time{}, false
	}
	for i := 0; i < n; i++ {
		e := ents[i*10:]
		tag := binary.LittleEndian.Uint16(e[0:2])
		size := int64(binary.LittleEndian.Uint32(e[2:6]))
		off := start + int64(binary.LittleEndian.Uint32(e[6:10]))
		if tag&0xc000 != 0 { // value stored in the record, not the heap
			continue
		}
		if dt := tag & 0x3800; dt == 0x2800 || dt == 0x3000 {
			if t, ok := ciffHeap(r, off, off+size, depth+1); ok {
				return t, true
			}
			continue
		}
		if tag&0x3fff == 0x180e {
			if b := r.bytes(off, 4); b != nil {
				t := time.Unix(int64(binary.LittleEndian.Uint32(b)), 0).UTC()
				return t, plausible(t)
			}
		}
	}
	return time.Time{}, false
}

// readMRW: Minolta MRW keeps a TIFF block named TTW inside its header.
func readMRW(r *rd) (time.Time, bool) {
	if h := r.bytes(0, 4); h == nil || string(h) != "\x00MRM" {
		return time.Time{}, false
	}
	hlen, _ := r.be32(4)
	p := int64(8)
	for p+8 <= 8+int64(hlen) {
		h := r.bytes(p, 8)
		if h == nil {
			break
		}
		l := int64(binary.BigEndian.Uint32(h[4:8]))
		if string(h[:4]) == "\x00TTW" {
			return readTIFF(r, p+8)
		}
		p += 8 + l
	}
	return time.Time{}, false
}

// readMTS: AVCHD streams carry a Sony/Panasonic "MDPM" block in H.264 SEI
// with BCD-coded local date (tag 0x18) and time (tag 0x19).
func readMTS(r *rd) (time.Time, bool) {
	n := int64(4 << 20)
	if n > r.size {
		n = r.size
	}
	buf := r.bytes(0, int(n))
	if buf == nil {
		return time.Time{}, false
	}
	i := bytes.Index(buf, []byte("MDPM"))
	if i < 0 || i+5 >= len(buf) {
		return time.Time{}, false
	}
	cnt := int(buf[i+4])
	p := i + 5
	var d18, d19 []byte
	for k := 0; k < cnt && p+5 <= len(buf); k++ {
		switch buf[p] {
		case 0x18:
			d18 = buf[p+1 : p+5]
		case 0x19:
			d19 = buf[p+1 : p+5]
		}
		p += 5
	}
	if d18 == nil || d19 == nil {
		return time.Time{}, false
	}
	bcd := func(b byte) int { return int(b>>4)*10 + int(b&15) }
	t := time.Date(bcd(d18[1])*100+bcd(d18[2]), time.Month(bcd(d18[3])), bcd(d19[0]),
		bcd(d19[1]), bcd(d19[2]), bcd(d19[3]), 0, time.UTC)
	return t, plausible(t) && t.Month() >= 1 && t.Month() <= 12
}

// readAVI: RIFF AVI files may have an IDIT chunk ("Mon Jan 23 17:21:59 2026")
// or an embedded EXIF block (Canon/Nikon/Fuji).
func readAVI(r *rd) (time.Time, bool) {
	n := int64(256 << 10)
	if n > r.size {
		n = r.size
	}
	buf := r.bytes(0, int(n))
	if buf == nil || len(buf) < 12 || string(buf[0:4]) != "RIFF" {
		return time.Time{}, false
	}
	if i := bytes.Index(buf, []byte("IDIT")); i >= 0 && i+8 < len(buf) {
		l := int(binary.LittleEndian.Uint32(buf[i+4 : i+8]))
		if l > 0 && l < 64 && i+8+l <= len(buf) {
			s := strings.TrimRight(string(buf[i+8:i+8+l]), "\x00\r\n ")
			for _, layout := range []string{"Mon Jan _2 15:04:05 2006", "Mon Jan 02 15:04:05 2006", "2006:01:02 15:04:05"} {
				if t, err := time.Parse(layout, s); err == nil && plausible(t) {
					return t, true
				}
			}
		}
	}
	return scanForExif(r)
}
