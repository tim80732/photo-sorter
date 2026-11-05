package sorter

import (
	"context"
	"errors"
	"fmt"
	"hash/maphash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
)

var (
	errExists      = errors.New("target exists")
	errCrossDevice = errors.New("different disk")
	errConflict    = errors.New("a different file with that name is already there")
)

// Result statuses.
const (
	StCopied    = "copied"
	StMoved     = "moved"
	StDuplicate = "duplicate"
	StFailed    = "failed"
	StNotRun    = "not run" // cancelled before reaching it
	StIgnored   = "ignored" // left alone by the plan; Reason says why
)

type Result struct {
	ID     int    `json:"id"`
	Src    string `json:"src"`
	Dest   string `json:"dest,omitempty"` // final path relative to the destination
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
}

type Summary struct {
	Started     time.Time `json:"started"`
	Finished    time.Time `json:"finished"`
	Copied      int       `json:"copied"`
	Moved       int       `json:"moved"`
	Duplicates  int       `json:"duplicates"`
	Failed      int       `json:"failed"`
	NotRun      int       `json:"notRun"`
	Renamed     int       `json:"renamed"` // got a _N suffix because the name was taken
	Bytes       int64     `json:"bytes"`
	CopiedBytes int64     `json:"copiedBytes"` // bytes actually written (renames excluded), for speed
	Elapsed     float64   `json:"elapsed"`     // seconds spent sorting (a stop and continue counts only the running time)
	Workers     int       `json:"workers"`     // files copied at the same time
	Check       string    `json:"check"`       // "disk" (read back from the disk) or "quick"; empty before the setting existed
	Cancelled   bool      `json:"cancelled"`
	Folders     []string  `json:"folders"` // folders created
}

type runner struct {
	ctx  context.Context
	p    *Plan
	root string
	prog *Progress

	mu      sync.Mutex // guards everything below
	months  map[string]bool
	cross   map[int]bool // sources known to be on another disk (rename impossible)
	sum     Summary
	results []Result
}

// A task is a photo followed by its sidecars (or a lone sidecar going next
// to a photo that is already in the archive). One worker runs a whole task,
// so sidecars always follow their photo's final name.
type task []*Item

// Run carries out an approved plan. Sources are done one after another;
// within a source several files are copied at once (see Workers). Each
// copy is verified before it counts, and nothing is ever overwritten.
//
// A sort that was stopped can be continued by calling Run again with the
// same plan and Progress: it does only the files not reached yet, and the
// progress carries on from where it stopped. The Summary and Results
// returned cover that call only.
func Run(ctx context.Context, p *Plan, prog *Progress) (Summary, []Result) {
	root, _ := filepath.Abs(p.Options.Dest)
	r := &runner{ctx: ctx, p: p, root: root, prog: prog, months: map[string]bool{}, cross: map[int]bool{}}
	r.sum.Started = time.Now()
	r.sum.Workers = Workers(p.Options)
	r.sum.Check = "disk"
	if p.Options.QuickCheck {
		r.sum.Check = "quick"
	}
	first := p.runs == 0
	p.runs++

	// Progress covers the whole plan; files done by an earlier call count as done.
	var total, doneBytes int64
	count, done := 0, 0
	srcTotal, srcDone := make([]int, len(p.Options.Sources)), make([]int, len(p.Options.Sources))
	for _, it := range p.Items {
		if it.Action != ActCopy {
			continue
		}
		total += it.Size
		count++
		srcTotal[it.Source]++
		if it.ran {
			doneBytes += it.Size
			done++
			srcDone[it.Source]++
		}
	}
	prog.phase("Sorting", count)
	prog.update(func(s *ProgressSnapshot) {
		s.Done, s.BytesDone = done, doneBytes
		s.BytesTotal, s.Sources, s.Workers = total, len(p.Options.Sources), r.sum.Workers
		s.Active = make([]ActiveFile, r.sum.Workers)
	})
	// Files the preview already settled are recorded too, so History and
	// Leftovers can say what this job did with every file it saw.
	if first {
		for _, it := range p.Items {
			switch it.Action {
			case ActDup:
				r.add(it, it.Match, StDuplicate, nil)
			case ActSkip:
				r.add(it, "", StIgnored, nil)
			}
		}
	}

	perSource := r.tasks()
	for si, ts := range perSource {
		prog.update(func(s *ProgressSnapshot) { s.Source, s.SourceDone, s.SourceTotal = si, srcDone[si], srcTotal[si] })
		var dirsMu sync.Mutex
		var movedDirs []string
		queue := make(chan task)
		var wg sync.WaitGroup
		for w := 0; w < r.sum.Workers; w++ {
			wg.Add(1)
			go func(slot int) {
				defer wg.Done()
				for t := range queue {
					moved := r.runTask(t, slot)
					dirsMu.Lock()
					movedDirs = append(movedDirs, moved...)
					dirsMu.Unlock()
				}
			}(w)
		}
		for _, t := range ts {
			queue <- t
		}
		close(queue)
		wg.Wait()
		if p.Options.Move {
			srcRoot, _ := filepath.Abs(p.Options.Sources[si])
			RemoveEmptyDirs(movedDirs, srcRoot, p.Options.Protected)
		}
	}
	r.sum.Cancelled = ctx.Err() != nil && r.sum.NotRun > 0
	r.sum.Finished = time.Now()
	r.sum.Elapsed = r.sum.Finished.Sub(r.sum.Started).Seconds()
	sort.Slice(r.results, func(i, j int) bool { return r.results[i].ID < r.results[j].ID })
	return r.sum, r.results
}

// runTask sorts a photo and its sidecars. It returns the source folders
// that files were moved out of.
func (r *runner) runTask(t task, slot int) []string {
	var moved []string
	photoOK, photoFinal := false, ""
	for i, it := range t {
		if r.ctx.Err() != nil {
			r.add(it, "", StNotRun, nil) // not counted as done: Continue picks it up
			continue
		}
		dest := it.Dest
		// A sidecar follows its photo's final name: the photo comes first in
		// its task, or was sorted by an earlier run that was then stopped.
		photo := t[0]
		resumed := i == 0 && it.Parent >= 0 && r.p.Items[it.Parent].Action == ActCopy
		if resumed {
			photo = r.p.Items[it.Parent]
			photoOK, photoFinal = photo.final != "", photo.final
		}
		if i > 0 || resumed {
			if !photoOK {
				r.add(it, "", StFailed, errors.New("its photo wasn't sorted"))
				r.stepDone(it, 0)
				continue
			}
			dest = sidecarDest(it, photo, photoFinal)
		}
		r.prog.update(func(s *ProgressSnapshot) {
			if slot < len(s.Active) {
				s.Active[slot] = ActiveFile{Rel: it.Rel}
			}
			s.Current = it.Rel
		})
		var counted int64
		status, rel, err := r.one(it, dest, func(n int64) {
			counted += n
			r.prog.update(func(s *ProgressSnapshot) { s.BytesDone += n })
		}, func() {
			r.prog.update(func(s *ProgressSnapshot) {
				if slot < len(s.Active) {
					s.Active[slot].Checking = true
				}
			})
		})
		r.add(it, rel, status, err)
		r.mu.Lock()
		r.sum.CopiedBytes += counted
		r.mu.Unlock()
		if i == 0 && !resumed {
			photoOK, photoFinal = status == StCopied || status == StMoved, rel
		}
		if status == StCopied || status == StMoved {
			if (it.Clash || rel != it.Dest) && it.Parent < 0 {
				r.mu.Lock()
				r.sum.Renamed++
				r.mu.Unlock()
			}
			if status == StMoved {
				moved = append(moved, filepath.Dir(it.Src))
			}
		}
		r.stepDone(it, counted)
	}
	r.prog.update(func(s *ProgressSnapshot) {
		if slot < len(s.Active) {
			s.Active[slot] = ActiveFile{}
		}
	})
	return moved
}

// stepDone counts a finished file; bytes not yet reported (renames,
// duplicates, failures) are added so the total reaches 100%.
func (r *runner) stepDone(it *Item, counted int64) {
	r.prog.update(func(s *ProgressSnapshot) {
		s.Done++
		s.SourceDone++
		s.BytesDone += it.Size - counted
	})
}

// tasks groups the files to sort per source: photos by date, each with its
// sidecars, then sidecars for photos already in the archive.
func (r *runner) tasks() [][]task {
	var photos []*Item
	side := map[int][]*Item{}
	var orphans []*Item
	for _, it := range r.p.Items {
		if it.Action != ActCopy || it.ran {
			continue
		}
		if it.Parent >= 0 {
			if par := r.p.Items[it.Parent]; par.Action == ActCopy && !par.ran {
				side[it.Parent] = append(side[it.Parent], it)
			} else {
				orphans = append(orphans, it)
			}
			continue
		}
		photos = append(photos, it)
	}
	sort.SliceStable(photos, func(i, j int) bool {
		a, b := photos[i], photos[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		return a.Rel < b.Rel
	})
	out := make([][]task, len(r.p.Options.Sources))
	for _, ph := range photos {
		out[ph.Source] = append(out[ph.Source], append(task{ph}, side[ph.ID]...))
	}
	for _, sc := range orphans {
		out[sc.Source] = append(out[sc.Source], task{sc})
	}
	return out
}

func (r *runner) add(it *Item, rel, status string, err error) {
	res := Result{ID: it.ID, Src: it.Src, Dest: rel, Status: status}
	if status == StIgnored {
		res.Reason = it.Reason
	}
	if err != nil {
		res.Error = err.Error()
	}
	r.mu.Lock()
	if it.Action == ActCopy && status != StNotRun {
		it.ran = true
		if status == StCopied || status == StMoved {
			it.final = rel
		}
	}
	r.results = append(r.results, res)
	switch status {
	case StCopied:
		r.sum.Copied++
		r.sum.Bytes += it.Size
	case StMoved:
		r.sum.Moved++
		r.sum.Bytes += it.Size
	case StDuplicate:
		r.sum.Duplicates++
	case StFailed:
		r.sum.Failed++
	case StNotRun:
		r.sum.NotRun++
	}
	r.mu.Unlock()
	r.prog.update(func(s *ProgressSnapshot) { s.Counts[status]++ })
}

// sidecarDest names a sidecar after its photo's final name, which can differ
// from the plan if a clash was found at run time.
func sidecarDest(sc, photo *Item, photoFinal string) string {
	if photoFinal == photo.Dest {
		return sc.Dest
	}
	name := pathBase(photoFinal)
	ext := filepath.Ext(sc.Src)
	if strings.HasPrefix(strings.ToLower(pathBase(sc.Dest)), strings.ToLower(pathBase(photo.Dest))+".") {
		return pathDir(photoFinal) + "/" + name + ext // <photo>.RAF.pp3 style
	}
	return pathDir(photoFinal) + "/" + strings.TrimSuffix(name, filepath.Ext(name)) + ext
}

// one sorts a single file. It returns the status, the final relative path,
// and an error for failures (or a warning with StCopied). onBytes reports
// copy progress; onCheck is called when the copy starts being checked.
func (r *runner) one(it *Item, destRel string, onBytes func(int64), onCheck func()) (string, string, error) {
	info, err := os.Stat(it.Src)
	if err != nil {
		return StFailed, "", fmt.Errorf("the original is gone: %v", err)
	}
	if info.Size() != it.Size {
		return StFailed, "", errors.New("the original changed since the preview; scan again")
	}
	dirRel := pathDir(destRel)
	dir := filepath.Join(r.root, filepath.FromSlash(dirRel))
	if r.p.Options.Tidy {
		// A rename creates only the folders it puts files in.
		if err := mkdirAllShared(dir); err != nil {
			return StFailed, "", err
		}
	} else {
		if err := r.ensureMonth(strings.SplitN(destRel, "/", 2)[0]); err != nil {
			return StFailed, "", err
		}
		if err := os.MkdirAll(dir, 0o777); err != nil {
			return StFailed, "", err
		}
	}
	name := pathBase(destRel)
	sidecar := it.Parent >= 0

	r.mu.Lock()
	tryRename := r.p.Options.Move && !r.cross[it.Source]
	r.mu.Unlock()
	if tryRename {
		n, dup, err := r.place(it.Src, dir, name, nil, sidecar)
		switch {
		case err == nil && dup:
			return StDuplicate, "", nil
		case err == nil:
			if st, err := os.Stat(filepath.Join(dir, n)); err != nil || st.Size() != it.Size {
				return StFailed, "", errors.New("the moved file can't be found at its new place")
			}
			return StMoved, dirRel + "/" + n, nil
		case errors.Is(err, errConflict):
			return StFailed, "", err
		case errors.Is(err, errCrossDevice):
			r.mu.Lock()
			r.cross[it.Source] = true
			r.mu.Unlock()
		}
		// Anything else (permissions, unsupported rename): fall back to copying.
	}

	tmp, err := copyToTemp(it.Src, dir, info, r.p.Options.Move, !r.p.Options.QuickCheck, onBytes, onCheck)
	if err != nil {
		return StFailed, "", err
	}
	n, dup, err := r.place(tmp, dir, name, nil, sidecar)
	if err != nil || dup {
		os.Remove(tmp)
		if dup {
			return StDuplicate, "", nil
		}
		return StFailed, "", err
	}
	rel := dirRel + "/" + n
	if r.p.Options.Move {
		if err := removeFile(it.Src); err != nil {
			return StCopied, rel, fmt.Errorf("copied and verified, but the original couldn't be removed: %v", err)
		}
		return StMoved, rel, nil
	}
	return StCopied, rel, nil
}

// place renames from into dir/name without overwriting. If the name is
// taken by an identical file it reports a duplicate; otherwise it tries
// name_1, name_2... (sidecars never get a suffix: that would detach them).
func (r *runner) place(from, dir, name string, sum *[32]byte, sidecar bool) (string, bool, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < 10000; i++ {
		n := name
		if i > 0 {
			n = stem + "_" + strconv.Itoa(i) + ext
		}
		target := filepath.Join(dir, n)
		err := renameNoReplace(from, target)
		if err == nil {
			return n, false, nil
		}
		if !errors.Is(err, errExists) {
			return "", false, err
		}
		if sum == nil {
			h, err := hashFile(from)
			if err != nil {
				return "", false, err
			}
			sum = &h
		}
		if h, err := hashFile(target); err == nil && h == *sum {
			return n, true, nil
		}
		if sidecar {
			return "", false, errConflict
		}
	}
	return "", false, errors.New("no free file name")
}

// Copy buffers are reused across files and workers.
const chunkSize = 4 << 20

var bufPool = sync.Pool{New: func() any { b := make([]byte, chunkSize); return &b }}

// Reads that bypass the system cache need a buffer aligned to the disk's
// sector size (4 KB covers every common disk), so these are cut from a
// slightly larger one.
var alignedPool = sync.Pool{New: func() any {
	b := make([]byte, chunkSize+4096)
	off := (4096 - int(uintptr(unsafe.Pointer(&b[0]))&4095)) & 4095
	b = b[off : off+chunkSize]
	return &b
}}

// Copies are verified with a fast 64-bit checksum (maphash, ~12 GB/s per
// core): any copy error changes it. Deciding that two files are the same
// (duplicates) still uses SHA-256.
var checkSeed = maphash.MakeSeed()

func checksumFile(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return checksumReader(f)
}

func checksumReader(f io.Reader) (uint64, error) {
	b := bufPool.Get().(*[]byte)
	defer bufPool.Put(b)
	var h maphash.Hash
	h.SetSeed(checkSeed)
	if _, err := io.CopyBuffer(&h, f, *b); err != nil {
		return 0, err
	}
	return h.Sum64(), nil
}

// copyToTemp copies src into a hidden temp file in dir, then reads the copy
// back and compares checksums. With diskCheck the copy is flushed to the
// disk and read back from the disk itself (checksumFromDisk), so a drive
// that stored it wrongly is caught too; otherwise the read-back usually
// comes from the system's memory. Moves (durable: the original is deleted
// next) are always flushed to the disk before they're checked.
func copyToTemp(src, dir string, info os.FileInfo, durable, diskCheck bool, onBytes func(int64), onCheck func()) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp := filepath.Join(dir, ".photosorter-"+newID()+".part")
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return "", err
	}
	// Reserving the full size first keeps parallel copies from fragmenting.
	if info.Size() > chunkSize {
		out.Truncate(info.Size())
	}
	want, n, err := pipeCopy(out, in, onBytes)
	if err == nil {
		err = out.Truncate(n) // in case the source shrank under us
	}
	if err == nil && (durable || diskCheck) {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != info.Size() {
		err = fmt.Errorf("copied %d of %d bytes", n, info.Size())
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	onCheck()
	var got uint64
	if diskCheck {
		got, err = checksumFromDisk(tmp)
	} else {
		got, err = checksumFile(tmp)
	}
	if err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("couldn't read the copy back to check it, so it was removed and the original was kept: %v", err)
	}
	if got != want {
		os.Remove(tmp)
		if diskCheck {
			return "", errors.New("the copy read back from the disk didn't match the original. The bad copy was removed and the original was kept. Run the job again to retry it; if it keeps failing, the destination drive may be faulty")
		}
		return "", errors.New("the copy didn't match the original (verify failed); the original was kept")
	}
	if err := copyTimes(tmp, info); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("couldn't set the file dates: %v", err)
	}
	finishNewFile(tmp)
	return tmp, nil
}

// pipeCopy reads ahead on one goroutine (checksumming as it reads) while
// this one writes, so the source and the destination are busy at once.
func pipeCopy(dst io.Writer, src io.Reader, onBytes func(int64)) (uint64, int64, error) {
	type block struct {
		buf *[]byte
		n   int
		err error
	}
	const depth = 3
	free := make(chan *[]byte, depth)
	full := make(chan block, depth)
	stop := make(chan struct{})
	for i := 0; i < depth; i++ {
		free <- bufPool.Get().(*[]byte)
	}
	var h maphash.Hash
	h.SetSeed(checkSeed)
	go func() {
		defer close(full)
		for {
			var b *[]byte
			select {
			case b = <-free:
			case <-stop:
				return
			}
			n, err := io.ReadFull(src, *b)
			if n > 0 {
				h.Write((*b)[:n])
			}
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				full <- block{b, n, nil}
				return
			}
			full <- block{b, n, err}
			if err != nil {
				return
			}
		}
	}()
	var total int64
	var werr error
	stopped := false
	for blk := range full {
		if werr == nil && blk.err != nil {
			werr = blk.err
		}
		if werr == nil && blk.n > 0 {
			if _, err := dst.Write((*blk.buf)[:blk.n]); err != nil {
				werr = err
			} else {
				total += int64(blk.n)
				onBytes(int64(blk.n))
			}
		}
		if werr != nil && !stopped {
			close(stop)
			stopped = true
		}
		free <- blk.buf
	}
	close(free)
	for b := range free {
		bufPool.Put(b)
	}
	return h.Sum64(), total, werr
}

// ensureMonth creates the month folder with JPG, RAW and MOV inside. An
// existing folder of the same name in any case is left as it is.
func (r *runner) ensureMonth(month string) error {
	if r.months[month] {
		return nil
	}
	if err := os.MkdirAll(r.root, 0o777); err != nil {
		return err
	}
	mdir := filepath.Join(r.root, month)
	if _, err := os.Stat(mdir); os.IsNotExist(err) {
		if err := mkdirShared(mdir); err != nil && !os.IsExist(err) {
			return err
		}
		r.sum.Folders = append(r.sum.Folders, month)
	}
	ents, err := os.ReadDir(mdir)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, e := range ents {
		if e.IsDir() {
			have[strings.ToLower(e.Name())] = true
		}
	}
	for _, c := range Cats {
		if !have[strings.ToLower(string(c))] {
			if err := mkdirShared(filepath.Join(mdir, string(c))); err != nil && !os.IsExist(err) {
				return err
			}
			r.sum.Folders = append(r.sum.Folders, month+"/"+string(c))
		}
	}
	r.months[month] = true
	return nil
}

// mkdirAllShared is os.MkdirAll with every new folder writable by everyone
// on the share, like the month folders an import creates.
func mkdirAllShared(dir string) error {
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return nil
	}
	if parent := filepath.Dir(dir); parent != dir {
		if err := mkdirAllShared(parent); err != nil {
			return err
		}
	}
	if err := mkdirShared(dir); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}

// RemoveEmptyDirs removes each folder that is now empty, then its parents,
// stopping at root and at protected folders (and their parents).
func RemoveEmptyDirs(dirs []string, root string, protected []string) {
	keep := map[string]bool{strings.ToLower(filepath.Clean(root)): true}
	for _, p := range protected {
		p = filepath.Clean(p)
		keep[strings.ToLower(p)] = true
		keep[strings.ToLower(filepath.Dir(p))] = true
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		for d = filepath.Clean(d); within(d, root) && !keep[strings.ToLower(d)]; d = filepath.Dir(d) {
			if os.Remove(d) != nil { // fails unless empty
				break
			}
		}
	}
}
