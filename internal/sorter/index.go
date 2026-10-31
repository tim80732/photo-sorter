package sorter

import (
	"bytes"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// destIndex caches what is already in the destination so that names and
// duplicates can be checked without touching the disk again for every file.
type destIndex struct {
	root   string
	months map[string]*monthInfo
	dirs   map[string]*destDir
	hashes *hashCache
}

type monthInfo struct {
	exists bool
	cats   map[string]string // lower-case name -> actual folder name
}

type cand struct {
	rel, abs string
	mtime    time.Time
	planned  bool // a file from this import, not yet in the destination
}

type destDir struct {
	ix    *destIndex
	rel   string
	names map[string]bool  // lower-case names directly in this folder (existing + planned)
	sizes map[int64][]cand // every file in this folder and below, by size
}

func newDestIndex(root string) *destIndex {
	return &destIndex{root: root, months: map[string]*monthInfo{}, dirs: map[string]*destDir{}, hashes: newHashCache()}
}

func (ix *destIndex) month(mo string) *monthInfo {
	if m, ok := ix.months[mo]; ok {
		return m
	}
	m := &monthInfo{cats: map[string]string{}}
	if ents, err := os.ReadDir(filepath.Join(ix.root, mo)); err == nil {
		m.exists = true
		for _, e := range ents {
			if e.IsDir() {
				m.cats[strings.ToLower(e.Name())] = e.Name()
			}
		}
	}
	ix.months[mo] = m
	return m
}

func (ix *destIndex) monthExists(mo string) bool { return ix.month(mo).exists }

// catDirName returns the folder to use for a type inside a month. An existing
// folder is reused whatever its case (so an old "raw" isn't doubled as "RAW").
func (ix *destIndex) catDirName(mo string, c Cat) (string, bool) {
	if n, ok := ix.month(mo).cats[strings.ToLower(string(c))]; ok {
		return n, true
	}
	return string(c), false
}

func (ix *destIndex) dir(mo string, c Cat) *destDir {
	name, _ := ix.catDirName(mo, c)
	return ix.dirByRel(mo + "/" + name)
}

func (ix *destIndex) dirByRel(rel string) *destDir {
	key := strings.ToLower(rel)
	if d, ok := ix.dirs[key]; ok {
		return d
	}
	d := &destDir{ix: ix, rel: rel, names: map[string]bool{}, sizes: map[int64][]cand{}}
	base := filepath.Join(ix.root, filepath.FromSlash(rel))
	filepath.WalkDir(base, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		r, _ := filepath.Rel(base, p)
		r = filepath.ToSlash(r)
		if !strings.Contains(r, "/") {
			d.names[strings.ToLower(e.Name())] = true
		}
		d.sizes[info.Size()] = append(d.sizes[info.Size()], cand{rel: rel + "/" + r, abs: p, mtime: info.ModTime()})
		return nil
	})
	ix.dirs[key] = d
	return d
}

func (d *destDir) taken(name string) bool { return d.names[strings.ToLower(name)] }

// unique returns name, or name_1, name_2... if it is taken.
func (d *destDir) unique(name string) (string, bool) {
	if !d.taken(name) {
		return name, false
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		n := stem + "_" + strconv.Itoa(i) + ext
		if !d.taken(n) {
			return n, true
		}
	}
}

func (d *destDir) reserve(name string, size int64, rel, src string, mtime time.Time) {
	d.names[strings.ToLower(name)] = true
	d.sizes[size] = append(d.sizes[size], cand{rel: rel, abs: src, mtime: mtime, planned: true})
}

// findSame looks for an identical file anywhere in this folder: either the
// files already there or (planned) the ones this import will add.
func (d *destDir) findSame(src string, size int64, mtime time.Time, planned bool) (string, bool) {
	for _, c := range d.sizes[size] {
		if c.planned == planned && d.ix.hashes.same(src, c.abs, mtime, c.mtime) {
			return c.rel, true
		}
	}
	return "", false
}

// findSameName checks whether the file called name in this folder is identical to src.
func (d *destDir) findSameName(src string, size int64, mtime time.Time, name string) (string, bool) {
	for _, c := range d.sizes[size] {
		if strings.EqualFold(c.rel, d.rel+"/"+name) && d.ix.hashes.same(src, c.abs, mtime, c.mtime) {
			return c.rel, true
		}
	}
	return "", false
}

// hashCache remembers SHA-256 hashes so each file is read at most once.
type hashCache struct {
	mu sync.Mutex
	m  map[string][32]byte
}

func newHashCache() *hashCache { return &hashCache{m: map[string][32]byte{}} }

func (h *hashCache) get(path string) ([32]byte, error) {
	h.mu.Lock()
	v, ok := h.m[path]
	h.mu.Unlock()
	if ok {
		return v, nil
	}
	v, err := hashFile(path)
	if err == nil {
		h.mu.Lock()
		h.m[path] = v
		h.mu.Unlock()
	}
	return v, err
}

// same reports whether two files have identical content. Callers have
// already checked that the sizes match.
//
// Like rsync's quick check (and stricter than Explorer, which compares only
// size and date): same size, same modified time and the same first and last
// 64 KB count as identical without reading the whole file. Copies made by
// this app, Explorer or rsync keep the modified time. When the times differ
// both files are hashed in full. Leftovers re-checks in full before deleting.
func (h *hashCache) same(a, b string, ta, tb time.Time) bool {
	if samePath(a, b) {
		return true
	}
	if !sameHead(a, b) {
		return false
	}
	if d := ta.Sub(tb); d > -2*time.Second && d < 2*time.Second && !ta.IsZero() { // FAT/exFAT keep 2 s steps
		return true
	}
	return h.fullySame(a, b)
}

// fullySame compares SHA-256 hashes of the whole files.
func (h *hashCache) fullySame(a, b string) bool {
	ha, err1 := h.get(a)
	hb, err2 := h.get(b)
	return err1 == nil && err2 == nil && ha == hb
}

// sameHead compares the first and last 64 KB, which rules out almost all
// different files of the same size without reading them whole.
func sameHead(a, b string) bool {
	read := func(p string) []byte {
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		const n = 64 << 10
		buf := make([]byte, 2*n)
		k, _ := io.ReadFull(f, buf[:n])
		if st, err := f.Stat(); err == nil && st.Size() > 2*n {
			m, _ := f.ReadAt(buf[n:], st.Size()-n)
			return buf[:n+m]
		}
		return buf[:k]
	}
	x, y := read(a), read(b)
	return x != nil && y != nil && bytes.Equal(x, y)
}

func hashFile(path string) ([32]byte, error) {
	var out [32]byte
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, 1<<20)); err != nil {
		return out, err
	}
	copy(out[:], h.Sum(nil))
	return out, nil
}
