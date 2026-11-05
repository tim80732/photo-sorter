package sorter

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A Rename of a messy archive: subfolders flattened, names dated, a photo in
// the wrong month moved, an unticked type gathered at the top, a duplicate
// and an unrelated file sent to misc, hidden folders left alone.
func TestTidy(t *testing.T) {
	root := t.TempDir()
	at := func(s string) time.Time {
		tm, _ := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local)
		return tm
	}
	write := func(rel, body, when string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o666); err != nil {
			t.Fatal(err)
		}
		if when != "" {
			os.Chtimes(p, at(when), at(when))
		}
	}
	write("202610/JPG/FUJI0001.JPG", "photo 1", "2026-10-08 09:42:11")
	write("202610/JPG/1/FUJI0002.JPG", "photo 2", "2026-10-09 10:00:00")
	write("202610/JPG/2/FUJI0001.JPG", "another photo 1", "2026-10-10 11:00:00")
	write("202610/JPG/FUJI0003-20261011-101010.JPG", "photo 3", "2026-10-11 10:10:10")
	write("202610/JPG/2/FUJI0003-20261011-101010.JPG", "photo 3", "2026-10-11 10:10:10") // duplicate
	write("202610/RAW/2/FUJI0002.RAF", "raw 2", "2026-10-09 10:00:00")
	write("202610/RAW/2/FUJI0002.xmp", "xmp 2", "2026-10-09 10:00:00")
	write("202610/MOV/FUJI0004.MOV", "clip", "2026-10-12 12:00:00")
	write("202610/FUJI0005.JPG", "photo 5", "2026-11-01 08:00:00")
	write("202609/JPG/FUJI0009-20260901-120000.JPG", "photo 9", "2026-09-01 12:00:00")
	write("Exports/notes.txt", "notes", "")
	write("misc/old.txt", "old", "")
	write(".stfolder/marker", "", "")
	os.MkdirAll(filepath.Join(root, "202609", "MOV"), 0o777) // empty folders stay

	opt := Options{Dest: root, Tidy: true, JPG: true, RAW: true}
	p, err := BuildPlan(context.Background(), opt, NewProgress())
	if err != nil {
		t.Fatal(err)
	}
	ti := p.Tidy
	if ti.Unticked[CatMOV] != 1 || ti.Duplicates != 1 || ti.ToMisc != 2 || ti.Removed != 5 {
		t.Errorf("summary: unticked %v, duplicates %d, to misc %d, removed %d", ti.Unticked, ti.Duplicates, ti.ToMisc, ti.Removed)
	}
	if ti.Renamed != 5 || ti.OtherMonth != 1 {
		t.Errorf("renamed %d (want 5), other month %d (want 1)", ti.Renamed, ti.OtherMonth)
	}

	sum, results := Run(context.Background(), p, NewProgress())
	if sum.Failed != 0 {
		for _, r := range results {
			if r.Status == StFailed {
				t.Errorf("failed: %s: %s", r.Src, r.Error)
			}
		}
	}
	want := map[string]string{
		"202610/JPG/FUJI0001-20261008-094211.JPG":                   "photo 1",
		"202610/JPG/FUJI0002-20261009-100000.JPG":                   "photo 2",
		"202610/JPG/FUJI0001-20261010-110000.JPG":                   "another photo 1",
		"202610/JPG/FUJI0003-20261011-101010.JPG":                   "photo 3",
		"misc/duplicates/202610/JPG/2/FUJI0003-20261011-101010.JPG": "photo 3",
		"202610/RAW/FUJI0002-20261009-100000.RAF":                   "raw 2",
		"202610/RAW/FUJI0002-20261009-100000.xmp":                   "xmp 2",
		"MOV/FUJI0004.MOV":                                          "clip",
		"202611/JPG/FUJI0005-20261101-080000.JPG":                   "photo 5",
		"202609/JPG/FUJI0009-20260901-120000.JPG":                   "photo 9",
		"misc/Exports/notes.txt":                                    "notes",
		"misc/old.txt":                                              "old",
		".stfolder/marker":                                          "",
	}
	for rel, body := range want {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || string(b) != body {
			t.Errorf("%s: got %q, %v", rel, b, err)
		}
	}
	for _, gone := range []string{"202610/JPG/1", "202610/JPG/2", "202610/RAW/2", "202610/MOV", "Exports"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(gone))); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "202609", "MOV")); err != nil {
		t.Errorf("an empty folder that was already empty should stay: %v", err)
	}

	// A second run finds nothing to do.
	p2, err := BuildPlan(context.Background(), opt, NewProgress())
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := p2.ToCopy(); n != 0 {
		for _, it := range p2.Items {
			if it.Action == ActCopy {
				t.Errorf("second run would move %s -> %s", it.Rel, it.Dest)
			}
		}
	}
}
