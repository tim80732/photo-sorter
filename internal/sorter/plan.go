package sorter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"photosorter/internal/meta"
)

// Options describe one import. Profiles store everything except Sources.
type Options struct {
	Sources  []string `json:"sources"`
	Dest     string   `json:"dest"`
	Move     bool     `json:"move"`
	JPG      bool     `json:"jpg"`
	RAW      bool     `json:"raw"`
	MOV      bool     `json:"mov"`
	Rename   bool     `json:"rename"`   // give files new names, built from Pattern
	Sidecars bool     `json:"sidecars"` // bring .xmp/.pp3/.dop along with their photo
	// Pattern is the new file name, e.g. "{Name}-{Date}-{Time}" (see
	// pattern.go). Empty means DefaultPattern.
	Pattern  string   `json:"pattern,omitempty"`
	Parallel int      `json:"parallel"` // files copied at once; 0 = automatic
	// QuickCheck compares each copy as the system still holds it in memory,
	// instead of reading it back from the disk (the default).
	QuickCheck bool `json:"quickCheck"`
	// Tidy is a Rename job: Dest is a parent folder that is sorted in place
	// (see tidy.go). JPG/RAW/MOV say which types get month folders.
	Tidy bool `json:"tidy,omitempty"`
	// Folders that are never removed when emptied (profile to-sort folders).
	Protected []string `json:"-"`
	// Only, if set, limits the scan to these paths (lower-case, relative to
	// their source). Used by the command line for benchmarks.
	Only map[string]bool `json:"-"`
}

func (o Options) includes(c Cat) bool {
	switch c {
	case CatJPG:
		return o.JPG
	case CatRAW:
		return o.RAW
	case CatMOV:
		return o.MOV
	}
	return false
}

// Actions and reasons for plan items.
const (
	ActCopy = "copy"      // will be copied or moved
	ActDup  = "duplicate" // identical file already at the destination
	ActSkip = "skip"      // left where it is; see Reason

	ReasonDuplicate = "duplicate" // Already in archive
	ReasonSystem    = "system"    // Camera system files
	ReasonExcluded  = "excluded"  // Left out by profile
	ReasonUnknown   = "unknown"   // Unknown files
	ReasonSidecar   = "sidecar"   // Sidecars not moved
	ReasonFailed    = "failed"    // Failed
)

// Item is one file found in a source.
type Item struct {
	ID       int    `json:"id"`
	Source   int    `json:"source"`
	Src      string `json:"src"` // absolute path
	Rel      string `json:"rel"` // path inside its source, for display
	Size     int64  `json:"size"`
	Cat      Cat    `json:"cat,omitempty"`
	Date     string `json:"date,omitempty"`     // capture date, wall clock
	Modified string `json:"modified,omitempty"` // the file's modified time
	DateSrc  string `json:"dateSrc,omitempty"`
	Fallback bool   `json:"fallback,omitempty"`
	Action   string `json:"action"`
	Reason   string `json:"reason,omitempty"`
	Dest     string `json:"dest,omitempty"`  // target, relative to Options.Dest, '/'-separated
	Match    string `json:"match,omitempty"` // for duplicates: the identical file, relative to Options.Dest
	Note     string `json:"note,omitempty"`
	Clash    bool   `json:"clash,omitempty"` // name was taken, so a _N suffix was added
	Parent   int    `json:"parent"`          // sidecars: ID of their photo, else -1

	date  time.Time
	mtime time.Time // modified time of the source file
	ext   string
	ran   bool   // a sort has handled it (anything but "not run")
	final string // where a sort put it, relative to Options.Dest
}

// Plan is the dry-run result that the user approves.
type Plan struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Options Options   `json:"options"`
	Items   []*Item   `json:"items"`
	Tree    []Month   `json:"tree"`
	Tidy    *TidyInfo `json:"tidy,omitempty"` // Rename jobs only

	runs int // sorts started on it; a stopped sort can be continued
}

// ToCopy counts the files and bytes the plan will copy or move.
func (p *Plan) ToCopy() (files int, bytes int64) {
	for _, it := range p.Items {
		if it.Action == ActCopy {
			files++
			bytes += it.Size
		}
	}
	return
}

// Month is one row of the preview tree.
type Month struct {
	Month  string    `json:"month"`
	Exists bool      `json:"exists"`
	Cats   []TreeCat `json:"cats"`
	Files  int       `json:"files"`
	Bytes  int64     `json:"bytes"`
}

type TreeCat struct {
	Cat    Cat    `json:"cat"`
	Dir    string `json:"dir"` // actual folder name (an existing "raw" is reused)
	Exists bool   `json:"exists"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
	Dups   int    `json:"dups"`
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// BuildPlan scans the sources and works out where every file would go.
// Nothing is written.
func BuildPlan(ctx context.Context, opt Options, prog *Progress) (*Plan, error) {
	if opt.Tidy {
		return buildTidyPlan(ctx, opt, prog)
	}
	if strings.TrimSpace(opt.Dest) == "" {
		return nil, fmt.Errorf("choose a destination folder")
	}
	if len(opt.Sources) == 0 {
		return nil, fmt.Errorf("add at least one source folder")
	}
	dest, _ := filepath.Abs(opt.Dest)
	p := &Plan{ID: newID(), Created: time.Now(), Options: opt}
	add := func(it *Item) { it.ID = len(p.Items); p.Items = append(p.Items, it) }

	// 1. Find files.
	prog.phase("Finding files", 0)
	prog.update(func(s *ProgressSnapshot) { s.Sources = len(opt.Sources) })
	var sidecars []*Item
	for si, src := range opt.Sources {
		root, _ := filepath.Abs(src)
		st, err := os.Stat(root)
		if err != nil || !st.IsDir() {
			return nil, fmt.Errorf("source folder not found: %s", src)
		}
		if within(root, dest) {
			return nil, fmt.Errorf("the source %s is inside the destination", src)
		}
		err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			rel, _ := filepath.Rel(root, path)
			if err != nil {
				if path != root {
					add(&Item{Source: si, Src: path, Rel: rel, Action: ActSkip, Reason: ReasonFailed, Note: "Couldn't read: " + err.Error(), Parent: -1})
				}
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if path != root && (SkipDir(d.Name()) || samePath(path, dest)) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || (opt.Only != nil && !opt.Only[strings.ToLower(rel)]) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			it := &Item{Source: si, Src: path, Rel: rel, Size: info.Size(), Parent: -1, ext: extOf(d.Name()), mtime: info.ModTime(),
				Modified: info.ModTime().In(time.Local).Format("2006-01-02 15:04:05")}
			kind, cat := classify(d.Name())
			switch kind {
			case kindMedia:
				it.Cat = cat
				if opt.includes(cat) {
					it.Action = ActCopy
					it.date = info.ModTime() // replaced by the capture date below
				} else {
					it.Action, it.Reason, it.Note = ActSkip, ReasonExcluded, string(cat)+" files are turned off in these settings"
				}
			case kindSidecar:
				it.Cat = CatSidecar
				sidecars = append(sidecars, it)
			case kindSystem:
				it.Action, it.Reason, it.Note = ActSkip, ReasonSystem, systemNote(d.Name())
			default:
				it.Action, it.Reason, it.Note = ActSkip, ReasonUnknown, unknownNote(d.Name())
			}
			add(it)
			prog.update(func(s *ProgressSnapshot) { s.Done++; s.Current = rel })
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	// 2. Read capture dates.
	var media []*Item
	for _, it := range p.Items {
		if it.Action == ActCopy {
			media = append(media, it)
		}
	}
	if err := readDates(ctx, media, prog); err != nil {
		return nil, err
	}

	// 3. Decide names and find duplicates, in a stable order.
	sort.SliceStable(media, func(i, j int) bool {
		a, b := media[i], media[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if !a.date.Equal(b.date) {
			return a.date.Before(b.date)
		}
		return a.Rel < b.Rel
	})
	// List the destination folders first, so the parallel check below only reads the index.
	ix := newDestIndex(dest)
	for _, it := range media {
		ix.dir(it.date.Format("200601"), it.Cat)
	}

	// Files already in the destination: checked in parallel, since each
	// check may read from both disks.
	prog.phase("Checking for duplicates", len(media))
	match := make([]string, len(media))
	idx := make(chan int)
	var wg2 sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			for i := range idx {
				it := media[i]
				if ctx.Err() == nil {
					if m, ok := ix.dir(it.date.Format("200601"), it.Cat).findSame(it.Src, it.Size, it.mtime, false); ok {
						match[i] = m
					}
				}
				prog.step(it.Rel)
			}
		}()
	}
	for i := range media {
		idx <- i
	}
	close(idx)
	wg2.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Names, and files that appear twice in this import, in a stable order.
	for i, it := range media {
		dir := ix.dir(it.date.Format("200601"), it.Cat)
		m := match[i]
		if m == "" {
			m, _ = dir.findSame(it.Src, it.Size, it.mtime, true)
		}
		if m != "" {
			it.Action, it.Reason, it.Match = ActDup, ReasonDuplicate, m
			continue
		}
		name := targetName(filepath.Base(it.Src), it.date, opt.namePattern())
		final, clash := dir.unique(name)
		it.Clash = clash
		it.Dest = dir.rel + "/" + final
		dir.reserve(final, it.Size, it.Dest, it.Src, it.mtime)
	}

	// 4. Sidecars follow their photo.
	attachSidecars(p, sidecars, ix, opt)

	p.Tree = buildTree(p, ix)
	runtime.GC()
	return p, nil
}

// readDates fills in the capture date of each file (in parallel: most of the
// time is I/O latency).
func readDates(ctx context.Context, todo []*Item, prog *Progress) error {
	prog.phase("Reading capture dates", len(todo))
	work := make(chan *Item)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range work {
				r := meta.Read(it.Src, it.date)
				it.date, it.DateSrc, it.Fallback = r.Time, r.Source, r.Fallback
				it.Date = r.Time.Format("2006-01-02 15:04:05")
				prog.step(it.Rel)
			}
		}()
	}
	for _, it := range todo {
		if ctx.Err() != nil {
			break
		}
		work <- it
	}
	close(work)
	wg.Wait()
	return ctx.Err()
}

func attachSidecars(p *Plan, sidecars []*Item, ix *destIndex, opt Options) {
	byStem := map[string][]*Item{}
	byName := map[string]*Item{}
	for _, it := range p.Items {
		if it.Cat == CatSidecar || it.Cat == "" {
			continue
		}
		dir := strings.ToLower(filepath.Dir(it.Src))
		base := strings.ToLower(filepath.Base(it.Src))
		byName[dir+"|"+base] = it
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		byStem[dir+"|"+stem] = append(byStem[dir+"|"+stem], it)
	}
	for _, sc := range sidecars {
		if !opt.Sidecars {
			sc.Action, sc.Reason, sc.Note = ActSkip, ReasonExcluded, "Sidecars are turned off in these settings"
			continue
		}
		dir := strings.ToLower(filepath.Dir(sc.Src))
		base := strings.ToLower(filepath.Base(sc.Src))
		inner := strings.TrimSuffix(base, filepath.Ext(base)) // "dscf0001" or "dscf0001.raf"
		var photo *Item
		if it, ok := byName[dir+"|"+inner]; ok { // DSCF0001.RAF.pp3
			photo = it
		} else {
			stem := strings.TrimSuffix(inner, filepath.Ext(inner))
			for _, it := range byStem[dir+"|"+stem] { // DSCF0001.xmp, prefer the RAW
				if photo == nil || (it.Cat == CatRAW && photo.Cat != CatRAW) {
					photo = it
				}
			}
		}
		sc.Action, sc.Reason = ActSkip, ReasonSidecar
		if photo == nil || (photo.Action != ActCopy && photo.Action != ActDup) {
			sc.Note = "No matching photo is being sorted"
			continue
		}
		sc.Parent = photo.ID
		sc.Cat = photo.Cat
		sc.date = photo.date
		sc.Date = photo.Date
		photoRel := photo.Dest
		if photo.Action == ActDup {
			photoRel = photo.Match
		}
		dirRel := pathDir(photoRel)
		photoName := pathBase(photoRel)
		var name string
		if strings.Count(inner, ".") > 0 && byName[dir+"|"+inner] == photo {
			name = photoName + filepath.Ext(sc.Src) // keep "<photo>.RAF.pp3" style
		} else {
			name = strings.TrimSuffix(photoName, filepath.Ext(photoName)) + filepath.Ext(sc.Src)
		}
		d := ix.dirByRel(dirRel)
		if d.taken(name) {
			if m, ok := d.findSameName(sc.Src, sc.Size, sc.mtime, name); ok {
				sc.Action, sc.Reason, sc.Match = ActDup, ReasonDuplicate, m
			} else {
				sc.Note = "A different sidecar is already next to this photo (" + dirRel + "/" + name + ")"
			}
			continue
		}
		sc.Action, sc.Reason = ActCopy, ""
		sc.Dest = dirRel + "/" + name
		d.reserve(name, sc.Size, sc.Dest, sc.Src, sc.mtime)
	}
}

func buildTree(p *Plan, ix *destIndex) []Month {
	months := map[string]*Month{}
	for _, it := range p.Items {
		if it.Action != ActCopy && it.Action != ActDup {
			continue
		}
		rel := it.Dest
		if it.Action == ActDup {
			if !strings.Contains(it.Match, "/") || it.Cat == CatSidecar {
				continue
			}
			rel = it.Match
		}
		mo := strings.SplitN(rel, "/", 2)[0]
		m := months[mo]
		if m == nil {
			m = &Month{Month: mo, Exists: ix.monthExists(mo)}
			for _, c := range Cats {
				name, exists := ix.catDirName(mo, c)
				m.Cats = append(m.Cats, TreeCat{Cat: c, Dir: name, Exists: exists})
			}
			months[mo] = m
		}
		c := it.Cat
		if c == CatSidecar {
			c = CatRAW
		}
		for i := range m.Cats {
			if m.Cats[i].Cat != c {
				continue
			}
			if it.Action == ActDup {
				m.Cats[i].Dups++
			} else {
				m.Cats[i].Files++
				m.Cats[i].Bytes += it.Size
				m.Files++
				m.Bytes += it.Size
			}
		}
	}
	var out []Month
	for _, m := range months {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Month < out[j].Month })
	return out
}

func systemNote(name string) string {
	switch extOf(name) {
	case "ctg":
		return "Camera file index"
	case "thm":
		return "Thumbnail"
	case "lrv", "lrf":
		return "Low-res video proxy"
	case "xml":
		return "Clip metadata"
	case "mpl", "cpi", "bdm":
		return "AVCHD index"
	}
	if strings.HasPrefix(name, "._") {
		return "macOS metadata"
	}
	return "System file"
}

func unknownNote(name string) string {
	if e := extOf(name); e != "" {
		return "." + e + " files aren't sorted"
	}
	return "No file extension"
}

// Path helpers. Destination-relative paths always use '/'.
func pathDir(rel string) string {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return ""
}

func pathBase(rel string) string { return rel[strings.LastIndex(rel, "/")+1:] }

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	if runtime.GOOS == "windows" {
		path, dir = strings.ToLower(path), strings.ToLower(dir)
	}
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}
