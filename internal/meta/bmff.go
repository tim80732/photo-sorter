package meta

import (
	"bytes"
	"encoding/binary"
	"time"
)

// ISO base media file format (MP4, MOV, CR3, HEIC/HIF).

type box struct {
	typ   string
	start int64 // first byte of the box header
	data  int64 // first byte after the header
	end   int64 // one past the last byte
}

// children lists the boxes in [start, end).
func children(r *rd, start, end int64) []box {
	var out []box
	p := start
	for p+8 <= end && len(out) < 4096 {
		h := r.bytes(p, 8)
		if h == nil {
			break
		}
		size := int64(binary.BigEndian.Uint32(h[:4]))
		typ := string(h[4:8])
		data := p + 8
		switch size {
		case 1:
			v, ok := r.be64(p + 8)
			if !ok {
				return out
			}
			size = int64(v)
			data = p + 16
		case 0:
			size = end - p
		}
		if size < data-p || p+size > end {
			break
		}
		out = append(out, box{typ: typ, start: p, data: data, end: p + size})
		p += size
	}
	return out
}

func find(bs []box, typ string) (box, bool) {
	for _, b := range bs {
		if b.typ == typ {
			return b, true
		}
	}
	return box{}, false
}

var canonUUID = []byte{0x85, 0xc0, 0xb6, 0x87, 0x82, 0x0f, 0x11, 0xe0, 0x81, 0x11, 0xf4, 0xce, 0x46, 0x2b, 0x6a, 0x48}

// canonCMT reads DateTimeOriginal from the CMT boxes Canon stores in
// moov/uuid (CR3 stills, and many Canon MP4/CRM clips).
func canonCMT(r *rd, moov box) (time.Time, bool) {
	for _, b := range children(r, moov.data, moov.end) {
		if b.typ != "uuid" {
			continue
		}
		if id := r.bytes(b.data, 16); id == nil || !bytes.Equal(id, canonUUID) {
			continue
		}
		kids := children(r, b.data+16, b.end)
		for _, name := range []string{"CMT2", "CMT1"} { // CMT2 = Exif IFD, CMT1 = IFD0
			if c, ok := find(kids, name); ok {
				if t, ok := readTIFF(r, c.data); ok {
					return t, true
				}
			}
		}
	}
	return time.Time{}, false
}

func readCR3(r *rd) (time.Time, bool) {
	if moov, ok := find(children(r, 0, r.size), "moov"); ok {
		return canonCMT(r, moov)
	}
	return time.Time{}, false
}

// readHEIF finds the Exif item through meta/iinf + meta/iloc.
func readHEIF(r *rd) (time.Time, bool) {
	meta, ok := find(children(r, 0, r.size), "meta")
	if !ok {
		return time.Time{}, false
	}
	kids := children(r, meta.data+4, meta.end) // meta is a FullBox
	iinf, ok1 := find(kids, "iinf")
	iloc, ok2 := find(kids, "iloc")
	if !ok1 || !ok2 {
		return time.Time{}, false
	}

	var exifID uint32
	ver, _ := r.u8(iinf.data)
	first := iinf.data + 4 + 2
	if ver > 0 {
		first = iinf.data + 4 + 4
	}
	for _, e := range children(r, first, iinf.end) {
		if e.typ != "infe" {
			continue
		}
		v, _ := r.u8(e.data)
		var id uint32
		var typAt int64
		switch {
		case v == 2:
			x, _ := r.be16(e.data + 4)
			id, typAt = uint32(x), e.data+4+2+2
		case v >= 3:
			id, _ = r.be32(e.data + 4)
			typAt = e.data + 4 + 4 + 2
		default:
			continue
		}
		if t := r.bytes(typAt, 4); t != nil && string(t) == "Exif" {
			exifID = id
			break
		}
	}
	if exifID == 0 {
		return time.Time{}, false
	}

	v, _ := r.u8(iloc.data)
	p := iloc.data + 4
	b1, _ := r.u8(p)
	b2, _ := r.u8(p + 1)
	offSize, lenSize := int(b1>>4), int(b1&15)
	baseSize, idxSize := int(b2>>4), 0
	if v == 1 || v == 2 {
		idxSize = int(b2 & 15)
	}
	p += 2
	var count uint32
	if v < 2 {
		c, _ := r.be16(p)
		count, p = uint32(c), p+2
	} else {
		count, _ = r.be32(p)
		p += 4
	}
	for i := uint32(0); i < count && i < 10000; i++ {
		var id uint32
		if v < 2 {
			x, ok := r.be16(p)
			if !ok {
				return time.Time{}, false
			}
			id, p = uint32(x), p+2
		} else {
			id, _ = r.be32(p)
			p += 4
		}
		method := uint16(0)
		if v == 1 || v == 2 {
			method, _ = r.be16(p)
			method &= 15
			p += 2
		}
		p += 2 // data_reference_index
		base, _ := r.beN(p, baseSize)
		p += int64(baseSize)
		ext, _ := r.be16(p)
		p += 2
		var firstOff uint64
		for j := uint16(0); j < ext; j++ {
			p += int64(idxSize)
			o, _ := r.beN(p, offSize)
			p += int64(offSize) + int64(lenSize)
			if j == 0 {
				firstOff = o
			}
		}
		if id == exifID && method == 0 {
			at := int64(base + firstOff)
			skip, ok := r.be32(at)
			if ok && skip < 64 {
				if t, ok := readTIFF(r, at+4+int64(skip)); ok {
					return t, true
				}
			}
			// Some writers get the offset wrong; look for the header nearby.
			if b := r.bytes(at, 64); b != nil {
				for _, sig := range [][]byte{[]byte("II*\x00"), []byte("MM\x00*")} {
					if k := bytes.Index(b, sig); k >= 0 {
						return readTIFF(r, at+int64(k))
					}
				}
			}
			return time.Time{}, false
		}
	}
	return time.Time{}, false
}

// videoInfo is what we can learn from a QuickTime/MP4 moov box.
type videoInfo struct {
	local    time.Time // wall-clock capture time from maker/Apple metadata
	localSrc string
	mvhd     time.Time // mvhd creation time, seconds since 1904 (meant to be UTC)
	duration time.Duration
}

func readQuickTime(r *rd) videoInfo {
	var vi videoInfo
	moov, ok := find(children(r, 0, r.size), "moov")
	if !ok {
		return vi
	}
	if t, ok := canonCMT(r, moov); ok {
		vi.local, vi.localSrc = t, "Canon metadata"
	}
	kids := children(r, moov.data, moov.end)
	if m, ok := find(kids, "mvhd"); ok {
		v, _ := r.u8(m.data)
		var ct, scale, dur uint64
		if v == 1 {
			ct, _ = r.be64(m.data + 4)
			s, _ := r.be32(m.data + 20)
			scale = uint64(s)
			dur, _ = r.be64(m.data + 24)
		} else {
			c, _ := r.be32(m.data + 4)
			s, _ := r.be32(m.data + 12)
			d, _ := r.be32(m.data + 16)
			ct, scale, dur = uint64(c), uint64(s), uint64(d)
		}
		if ct > 0 {
			vi.mvhd = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(ct) * time.Second)
		}
		if scale > 0 {
			vi.duration = time.Duration(float64(dur) / float64(scale) * float64(time.Second))
		}
	}
	if vi.local.IsZero() {
		if m, ok := find(kids, "meta"); ok {
			if t, ok := appleKeysDate(r, m); ok {
				vi.local, vi.localSrc = t, "Apple metadata"
			}
		}
	}
	if vi.local.IsZero() {
		if u, ok := find(kids, "udta"); ok {
			for _, b := range children(r, u.data, u.end) {
				switch b.typ {
				case "\xa9day":
					// 16-bit length, 16-bit language, then an ISO 8601 string
					if s := r.bytes(b.data+4, int(b.end-b.data-4)); s != nil {
						if t, ok := parseExifDate(string(s)); ok {
							vi.local, vi.localSrc = t, "QuickTime date tag"
						}
					}
				case "MVTG": // Fujifilm: a bare little-endian IFD 16 bytes in
					if t, ok := ifdDates(r, binary.LittleEndian, b.data+16, 0); ok {
						vi.local, vi.localSrc = t, "Fujifilm metadata"
					}
				case "NCDT": // Nikon
					if t, ok := nikonNCTG(r, b); ok {
						vi.local, vi.localSrc = t, "Nikon metadata"
					}
				case "meta":
					if t, ok := appleKeysDate(r, b); ok {
						vi.local, vi.localSrc = t, "Apple metadata"
					}
				}
				if !vi.local.IsZero() {
					break
				}
			}
		}
	}
	return vi
}

// appleKeysDate reads com.apple.quicktime.creationdate from a meta box
// (keys + ilst). The value keeps the capture time zone, so its wall clock
// is the local capture time.
func appleKeysDate(r *rd, m box) (time.Time, bool) {
	start := m.data
	if v, ok := r.be32(m.data); ok && v == 0 { // MP4-style meta is a FullBox
		start += 4
	}
	kids := children(r, start, m.end)
	keys, ok1 := find(kids, "keys")
	ilst, ok2 := find(kids, "ilst")
	if !ok1 || !ok2 {
		return time.Time{}, false
	}
	n, _ := r.be32(keys.data + 4)
	p := keys.data + 8
	want := uint32(0)
	for i := uint32(1); i <= n && i < 512; i++ {
		sz, ok := r.be32(p)
		if !ok || sz < 8 {
			break
		}
		if k := r.bytes(p+8, int(sz)-8); k != nil && string(k) == "com.apple.quicktime.creationdate" {
			want = i
			break
		}
		p += int64(sz)
	}
	if want == 0 {
		return time.Time{}, false
	}
	for _, it := range children(r, ilst.data, ilst.end) {
		if binary.BigEndian.Uint32([]byte(it.typ)) != want {
			continue
		}
		if d, ok := find(children(r, it.data, it.end), "data"); ok {
			if s := r.bytes(d.data+8, int(d.end-d.data-8)); s != nil {
				return parseExifDate(string(s))
			}
		}
	}
	return time.Time{}, false
}

// nikonNCTG reads DateTimeOriginal (tag 0x13) from Nikon's NCDT/NCTG box.
func nikonNCTG(r *rd, ncdt box) (time.Time, bool) {
	tg, ok := find(children(r, ncdt.data, ncdt.end), "NCTG")
	if !ok {
		return time.Time{}, false
	}
	size := map[uint16]int64{1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 7: 1, 8: 2, 9: 4, 10: 8}
	p := tg.data
	for i := 0; i < 256 && p+8 <= tg.end; i++ {
		h := r.bytes(p, 8)
		if h == nil {
			break
		}
		tag := binary.BigEndian.Uint32(h[0:4])
		typ := binary.BigEndian.Uint16(h[4:6])
		cnt := int64(binary.BigEndian.Uint16(h[6:8]))
		n := size[typ] * cnt
		if n == 0 {
			break
		}
		if (tag == 0x13 || tag == 0x14) && typ == 2 {
			if s := r.bytes(p+8, int(n)); s != nil {
				if t, ok := parseExifDate(string(s)); ok {
					return t, true
				}
			}
		}
		p += 8 + n
	}
	return time.Time{}, false
}
