package sorter

import (
	"os"
	"path/filepath"
)

// Space is what a sort needs on its destination and what is free there.
type Space struct {
	Free   uint64 `json:"free"`
	Needed int64  `json:"needed"`
}

// Fits reports whether the sort fits in the free space.
func (s Space) Fits() bool { return s.Needed <= 0 || uint64(s.Needed) <= s.Free }

// SpaceFor works out the space a plan needs on its destination. A move
// within one disk is a rename and needs none. ok is false when the free
// space can't be read.
func SpaceFor(p *Plan) (sp Space, ok bool) {
	dest := existingDir(p.Options.Dest)
	free, err := freeSpace(dest)
	if err != nil {
		return sp, false
	}
	sp.Free = free
	rename := make([]bool, len(p.Options.Sources))
	if p.Options.Move {
		for i, src := range p.Options.Sources {
			rename[i] = sameDisk(src, dest)
		}
	}
	for _, it := range p.Items {
		if it.Action == ActCopy && !rename[it.Source] {
			sp.Needed += it.Size
		}
	}
	return sp, true
}

// existingDir returns p, or its nearest parent that exists (the destination
// may not have been created yet).
func existingDir(p string) string {
	for {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}
