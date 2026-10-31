package sorter

import (
	"errors"
	"os"
	"path/filepath"
)

type DeleteResult struct {
	ID    int    `json:"id"`
	Src   string `json:"src"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// DeleteItems deletes files picked on the Leftovers page. Only files from
// the scan can be named, a file that changed since the scan is kept, and a
// duplicate is only deleted after a full byte-for-byte comparison with its
// archive copy.
func DeleteItems(p *Plan, ids []int) []DeleteResult {
	var out []DeleteResult
	dirs := map[int][]string{}
	for _, id := range ids {
		if id < 0 || id >= len(p.Items) {
			continue
		}
		it := p.Items[id]
		res := DeleteResult{ID: id, Src: it.Src}
		err := func() error {
			st, err := os.Stat(it.Src)
			if err != nil {
				return errors.New("already gone")
			}
			if st.Size() != it.Size {
				return errors.New("changed since the scan; rescan first")
			}
			if it.Action == ActDup {
				m := filepath.Join(p.Options.Dest, filepath.FromSlash(it.Match))
				ms, err := os.Stat(m)
				if err != nil || ms.Size() != it.Size {
					return errors.New("its archive copy is missing now, so it was kept")
				}
				// The scan's quick check is enough to skip a copy; deleting
				// needs proof, so compare the whole files.
				a, err1 := hashFile(it.Src)
				b, err2 := hashFile(m)
				if err1 != nil || err2 != nil {
					return errors.New("couldn't read both files to compare them, so it was kept")
				}
				if a != b {
					return errors.New("not identical to the archive copy after all, so it was kept")
				}
			}
			return removeFile(it.Src)
		}()
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
			dirs[it.Source] = append(dirs[it.Source], filepath.Dir(it.Src))
		}
		out = append(out, res)
	}
	for si, ds := range dirs {
		root, _ := filepath.Abs(p.Options.Sources[si])
		RemoveEmptyDirs(ds, root, p.Options.Protected)
	}
	return out
}
