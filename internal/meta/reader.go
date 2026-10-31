// Package meta reads capture dates from photo and video files without
// external tools. Every reader works on a small block cache so that only a
// few KB of each file are read, which keeps scans fast over SMB.
package meta

import (
	"encoding/binary"
	"io"
	"os"
)

const blockSize = 64 * 1024

// rd is a random-access reader with a small block cache.
type rd struct {
	f      *os.File
	size   int64
	blocks map[int64][]byte
}

func openRd(path string) (*rd, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &rd{f: f, size: st.Size(), blocks: map[int64][]byte{}}, nil
}

func (r *rd) Close() { r.f.Close() }

func (r *rd) block(n int64) []byte {
	if b, ok := r.blocks[n]; ok {
		return b
	}
	if len(r.blocks) > 64 { // cap memory; scans rarely need more
		r.blocks = map[int64][]byte{}
	}
	buf := make([]byte, blockSize)
	k, err := r.f.ReadAt(buf, n*blockSize)
	if err != nil && err != io.EOF {
		k = 0
	}
	buf = buf[:k]
	r.blocks[n] = buf
	return buf
}

// bytes returns n bytes at off, or nil if the range is outside the file.
func (r *rd) bytes(off int64, n int) []byte {
	if off < 0 || n < 0 || off+int64(n) > r.size {
		return nil
	}
	out := make([]byte, 0, n)
	for len(out) < n {
		pos := off + int64(len(out))
		b := r.block(pos / blockSize)
		i := int(pos % blockSize)
		if i >= len(b) {
			return nil
		}
		take := n - len(out)
		if take > len(b)-i {
			take = len(b) - i
		}
		out = append(out, b[i:i+take]...)
	}
	return out
}

func (r *rd) u8(off int64) (byte, bool) {
	b := r.bytes(off, 1)
	if b == nil {
		return 0, false
	}
	return b[0], true
}

func (r *rd) be16(off int64) (uint16, bool) {
	b := r.bytes(off, 2)
	if b == nil {
		return 0, false
	}
	return binary.BigEndian.Uint16(b), true
}

func (r *rd) be32(off int64) (uint32, bool) {
	b := r.bytes(off, 4)
	if b == nil {
		return 0, false
	}
	return binary.BigEndian.Uint32(b), true
}

func (r *rd) be64(off int64) (uint64, bool) {
	b := r.bytes(off, 8)
	if b == nil {
		return 0, false
	}
	return binary.BigEndian.Uint64(b), true
}

// beN reads a big-endian unsigned integer of 0, 4 or 8 bytes (used by iloc).
func (r *rd) beN(off int64, n int) (uint64, bool) {
	switch n {
	case 0:
		return 0, true
	case 4:
		v, ok := r.be32(off)
		return uint64(v), ok
	case 8:
		return r.be64(off)
	case 2:
		v, ok := r.be16(off)
		return uint64(v), ok
	}
	return 0, false
}
