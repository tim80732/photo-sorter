package sorter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"photosorter/internal/meta"
)

// Rename ("tidy") sorts a folder that is already an archive, in place:
// everything under it ends up in <parent>/yyyymm/<TYPE>, named with the
// capture date-time. Types that aren't ticked go to <parent>/<TYPE> with
// their names unchanged, and anything that isn't a photo, video or sidecar
// goes to <parent>/misc, keeping its path. Nothing is deleted or overwritten;
// folders left empty are removed.

// MiscDir is the folder, directly under the parent, that unrelated files go
// to. It is never sorted itself.
const MiscDir = "misc"

// Reasons for files a rename leaves where they are.
const (
	ReasonInPlace = "inplace" // already where it belongs
	ReasonMisc    = "misc"    // inside the misc folder
)

// TidyInfo is what the Rename preview shows besides the file list.
type TidyInfo struct {
	Before     []TreeDir   `json:"before"`     // every folder now, with the files directly in it
	After      []TreeDir   `json:"after"`      // the same after the rename
	Renamed    int         `json:"renamed"`    // photos and videos that get a new name or place
	OtherMonth int         `json:"otherMonth"` // of those, moving to a different month folder
	ToMisc     int         `json:"toMisc"`     // files going to misc (duplicates included)
	Duplicates int         `json:"duplicates"` // identical copies of another file, going to misc/duplicates
	Removed    int         `json:"removed"`    // folders left empty and removed
	Unticked   map[Cat]int `json:"unticked"`   // photos and videos of types that weren't ticked
	NoDate     int         `json:"noDate"`     // dated from the file name or modified time
}

// TreeDir is one folder of a preview tree: its path relative to the parent
// ('/'-separated, "" for the parent itself) and the files directly in it.
type TreeDir struct {
	Path  string `json:"path"`
	Files int    `json:"files"`
}

var monthName = regexp.MustCompile(`^\d{6}$`)

func buildTidyPlan(ctx context.Context, opt Options, prog *Progress) (*Plan, error) {
	if strings.TrimSpace(opt.Dest) == "" {
		return nil, fmt.Errorf("choose a parent folder")
	}
	if !opt.JPG && !opt.RAW && !opt.MOV {
		return nil, fmt.Errorf("tick at least one file type")
	}
	root, _ := filepath.Abs(opt.Dest)
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("folder not found: %s", opt.Dest)
	}
	opt.Dest, opt.Sources = root, []string{root}
	opt.Move, opt.Rename, opt.Sidecars = true, true, true
	p := &Plan{ID: newID(), Created: time.Now(), Options: opt}
	add := func(it *Item) { it.ID = len(p.Items); p.Items = append(p.Items, it) }

	// 1. Find files. Hidden files and folders (.stfolder, .DS_Store...) are
	// left alone and not shown.
	prog.phase("Finding files", 0)
	prog.update(func(s *ProgressSnapshot) { s.Sources = 1 })
	dirs := map[string]string{"": ""} // lower-case rel -> rel as it is on disk
	var sidecars, others []*Item
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if path == root {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if err != nil {
			add(&Item{Src: path, Rel: rel, Action: ActSkip, Reason: ReasonFailed, Note: "Couldn't read: " + err.Error(), Parent: -1})
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") && !strings.HasPrefix(strings.ToLower(d.Name()), ".photosorter-") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if SkipDir(d.Name()) {
				return filepath.SkipDir
			}
			dirs[strings.ToLower(rel)] = rel
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		it := &Item{Src: path, Rel: rel, Size: info.Size(), Parent: -1, ext: extOf(d.Name()), mtime: info.ModTime(),
			Modified: info.ModTime().In(time.Local).Format("2006-01-02 15:04:05")}
		add(it)
		prog.update(func(s *ProgressSnapshot) { s.Done++; s.Current = rel })
		if strings.EqualFold(strings.SplitN(rel, "/", 2)[0], MiscDir) && strings.Contains(rel, "/") {
			it.Action, it.Reason = ActSkip, ReasonMisc
			return nil
		}
		kind, cat := classify(d.Name())
		switch kind {
		case kindMedia:
			it.Cat, it.Action = cat, ActCopy
			it.date = info.ModTime() // replaced by the capture date below
		case kindSidecar:
			it.Cat = CatSidecar
			sidecars = append(sidecars, it)
		default:
			others = append(others, it)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 2. Capture dates of the photos and videos being renamed.
	var media []*Item
	unticked := map[Cat]int{}
	for _, it := range p.Items {
		if it.Action != ActCopy {
			continue
		}
		if opt.includes(it.Cat) {
			media = append(media, it)
		} else {
			unticked[it.Cat]++
		}
	}
	prog.phase("Reading capture dates", len(media))
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
	for _, it := range media {
		if ctx.Err() != nil {
			break
		}
		work <- it
	}
	close(work)
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// 3. Where each file belongs.
	prog.phase("Working out new names", 0)
	t := &tidy{root: root, dirs: dirs, names: map[string]map[string][]*Item{}, hashes: newHashCache()}
	type want struct {
		it        *Item
		dir, name string
	}
	var wants []want
	for _, it := range p.Items {
		if it.Action != ActCopy {
			continue
		}
		var dir, name string
		if opt.includes(it.Cat) {
			dir = t.dirName(it.date.Format("200601") + "/" + string(it.Cat))
			name = targetName(pathBase(it.Rel), it.date, true)
		} else {
			dir, name = t.dirName(string(it.Cat)), pathBase(it.Rel)
		}
		if strings.EqualFold(it.Rel, dir+"/"+name) {
			it.Action, it.Reason = ActSkip, ReasonInPlace
		} else {
			wants = append(wants, want{it, dir, name})
		}
	}
	// Sidecars stay next to their photo; anything else stays only in misc.
	photoOf := matchSidecars(p, sidecars)
	for _, sc := range sidecars {
		if ph := photoOf[sc]; ph != nil && ph.Action == ActSkip && strings.EqualFold(sc.Rel, pathDir(ph.Rel)+"/"+sidecarName(sc, ph, pathBase(ph.Rel))) {
			sc.Action, sc.Reason, sc.Parent, sc.Cat = ActSkip, ReasonInPlace, ph.ID, ph.Cat
		}
	}
	// Files staying where they are keep their names.
	for _, it := range p.Items {
		if it.Action == ActSkip && it.Reason != ReasonFailed {
			t.reserve(pathDir(it.Rel), pathBase(it.Rel), it)
		}
	}

	// Photos and videos, oldest first, so a clash gives the later one the _1.
	sort.SliceStable(wants, func(i, j int) bool {
		a, b := wants[i].it, wants[j].it
		if !a.date.Equal(b.date) {
			return a.date.Before(b.date)
		}
		return a.Rel < b.Rel
	})
	prog.phase("Checking for duplicates", len(wants))
	for _, w := range wants {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		prog.step(w.it.Rel)
		if m := t.identical(w.dir, w.name, w.it); m != nil {
			t.toMisc(w.it, "duplicates/"+w.it.Rel, "Identical to "+m.Rel)
			continue
		}
		final, clash := t.unique(w.dir, w.name)
		w.it.Dest, w.it.Clash = w.dir+"/"+final, clash
		t.reserve(w.dir, final, w.it)
	}

	// Sidecars follow their photo's new name.
	for _, sc := range sidecars {
		if sc.Action == ActSkip {
			continue
		}
		ph := photoOf[sc]
		if ph == nil {
			t.toMisc(sc, sc.Rel, "No matching photo")
			continue
		}
		phRel := ph.Rel
		if ph.Action == ActCopy {
			phRel = ph.Dest
		}
		dir, name := pathDir(phRel), sidecarName(sc, ph, pathBase(phRel))
		if t.taken(dir, name) {
			if m := t.identical(dir, name, sc); m != nil {
				t.toMisc(sc, "duplicates/"+sc.Rel, "Identical to "+m.Rel)
			} else {
				t.toMisc(sc, sc.Rel, "A different sidecar is already next to its photo")
			}
			continue
		}
		sc.Action, sc.Cat, sc.Dest = ActCopy, ph.Cat, dir+"/"+name
		if ph.Action == ActCopy {
			sc.Parent = ph.ID
		}
		t.reserve(dir, name, sc)
	}
	for _, it := range others {
		t.toMisc(it, it.Rel, "")
	}

	p.Tidy = t.summary(p, unticked)
	runtime.GC()
	return p, nil
}

// tidy keeps track of the names in every folder while a rename is planned.
type tidy struct {
	root   string
	dirs   map[string]string             // folders on disk: lower-case rel -> actual rel
	names  map[string]map[string][]*Item // lower-case folder -> lower-case name -> files
	hashes *hashCache
}

// dirName returns rel as it's spelled on disk if a folder of that name
// exists in any case (an old "raw" is reused, not doubled as "RAW").
func (t *tidy) dirName(rel string) string {
	if d, ok := t.dirs[strings.ToLower(rel)]; ok {
		return d
	}
	return rel
}

func (t *tidy) reserve(dir, name string, it *Item) {
	k := strings.ToLower(dir)
	if t.names[k] == nil {
		t.names[k] = map[string][]*Item{}
	}
	n := strings.ToLower(name)
	t.names[k][n] = append(t.names[k][n], it)
}

func (t *tidy) taken(dir, name string) bool {
	return len(t.names[strings.ToLower(dir)][strings.ToLower(name)]) > 0
}

func (t *tidy) unique(dir, name string) (string, bool) {
	if !t.taken(dir, name) {
		return name, false
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		n := stem + "_" + strconv.Itoa(i) + ext
		if !t.taken(dir, n) {
			return n, true
		}
	}
}

// identical returns a file that already has dir/name (or will) and the same
// content as it.
func (t *tidy) identical(dir, name string, it *Item) *Item {
	for _, o := range t.names[strings.ToLower(dir)][strings.ToLower(name)] {
		if o.Size == it.Size && t.hashes.same(it.Src, o.Src, it.mtime, o.mtime) {
			return o
		}
	}
	return nil
}

// toMisc sends a file to misc/<rel>, with a _N suffix if the name is taken.
func (t *tidy) toMisc(it *Item, rel, note string) {
	dir := MiscDir
	if d := pathDir(rel); d != "" {
		dir += "/" + d
	}
	dir = t.dirName(dir)
	final, clash := t.unique(dir, pathBase(rel))
	it.Action, it.Reason, it.Parent = ActCopy, "", -1
	if strings.HasPrefix(rel, "duplicates/") {
		it.Reason = ReasonDuplicate
	}
	if it.Cat == CatSidecar {
		it.Cat = ""
	}
	it.Dest, it.Clash, it.Note = dir+"/"+final, clash, note
	t.reserve(dir, final, it)
}

// matchSidecars finds the photo each sidecar belongs to, in the same folder:
// DSCF0001.RAF.pp3 by full name, else DSCF0001.xmp by stem (the RAW first).
func matchSidecars(p *Plan, sidecars []*Item) map[*Item]*Item {
	byStem := map[string][]*Item{}
	byName := map[string]*Item{}
	for _, it := range p.Items {
		if it.Cat == "" || it.Cat == CatSidecar || (it.Action != ActCopy && it.Reason != ReasonInPlace) {
			continue
		}
		dir := strings.ToLower(pathDir(it.Rel))
		base := strings.ToLower(pathBase(it.Rel))
		byName[dir+"|"+base] = it
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		byStem[dir+"|"+stem] = append(byStem[dir+"|"+stem], it)
	}
	out := map[*Item]*Item{}
	for _, sc := range sidecars {
		dir := strings.ToLower(pathDir(sc.Rel))
		inner := strings.TrimSuffix(strings.ToLower(pathBase(sc.Rel)), "."+sc.ext)
		if it, ok := byName[dir+"|"+inner]; ok {
			out[sc] = it
			continue
		}
		stem := strings.TrimSuffix(inner, filepath.Ext(inner))
		for _, it := range byStem[dir+"|"+stem] {
			if out[sc] == nil || (it.Cat == CatRAW && out[sc].Cat != CatRAW) {
				out[sc] = it
			}
		}
	}
	return out
}

// sidecarName is a sidecar's name next to its photo called photoName:
// "<photo>.RAF.pp3" style is kept, otherwise "<photo stem>.xmp".
func sidecarName(sc, photo *Item, photoName string) string {
	ext := filepath.Ext(sc.Rel)
	if strings.EqualFold(strings.TrimSuffix(pathBase(sc.Rel), ext), pathBase(photo.Rel)) {
		return photoName + ext
	}
	return strings.TrimSuffix(photoName, filepath.Ext(photoName)) + ext
}

// summary counts what the rename does and builds the before and after trees.
func (t *tidy) summary(p *Plan, unticked map[Cat]int) *TidyInfo {
	info := &TidyInfo{Unticked: unticked}
	before, after := map[string]int{}, map[string]int{}
	for k := range t.dirs {
		before[k] = 0
	}
	display := map[string]string{} // lower-case rel -> rel as shown
	for k, v := range t.dirs {
		display[k] = v
	}
	// Folders that hold files now, and that will after (with all their parents).
	hadFiles, hasFiles := map[string]bool{}, map[string]bool{}
	mark := func(m map[string]bool, dir string) {
		for d := dir; ; d = pathDir(d) {
			m[d] = true
			if d == "" {
				return
			}
		}
	}
	for _, it := range p.Items {
		from := strings.ToLower(pathDir(it.Rel))
		before[from]++
		mark(hadFiles, from)
		final := it.Rel
		if it.Action == ActCopy {
			final = it.Dest
		}
		to := pathDir(final)
		k := strings.ToLower(to)
		after[k]++
		if _, ok := display[k]; !ok {
			display[k] = to
		}
		for d := pathDir(to); d != ""; d = pathDir(d) {
			if _, ok := display[strings.ToLower(d)]; !ok {
				display[strings.ToLower(d)] = d
			}
		}
		mark(hasFiles, k)

		if it.Action != ActCopy {
			continue
		}
		if it.Fallback {
			info.NoDate++
		}
		switch {
		case strings.HasPrefix(strings.ToLower(it.Dest), MiscDir+"/"):
			info.ToMisc++
			if it.Reason == ReasonDuplicate {
				info.Duplicates++
			}
		case p.Options.includes(it.Cat) && IsMediaName(it.Rel):
			info.Renamed++
			if strings.SplitN(it.Rel, "/", 2)[0] != strings.SplitN(it.Dest, "/", 2)[0] {
				info.OtherMonth++
			}
		}
	}
	// A folder that held files and won't hold any is removed after the rename.
	// Folders that were empty already are left (and so are their parents).
	keep := map[string]bool{}
	for k := range hasFiles {
		keep[k] = true
	}
	for k := range before {
		if !hadFiles[k] {
			mark(keep, k)
		}
	}
	for k := range before {
		if !keep[k] {
			info.Removed++
		}
	}
	for k := range keep {
		if _, ok := after[k]; !ok {
			after[k] = 0
		}
	}
	list := func(m map[string]int) []TreeDir {
		out := make([]TreeDir, 0, len(m))
		for k, n := range m {
			out = append(out, TreeDir{Path: display[k], Files: n})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
		return out
	}
	info.Before, info.After = list(before), list(after)
	return info
}
