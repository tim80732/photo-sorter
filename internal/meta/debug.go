package meta

import (
	"fmt"
	"io"
	"strings"
)

// DumpBoxes prints the ISO-BMFF box tree of a file (for diagnosing new
// camera formats with `photosorter meta --boxes`).
func DumpBoxes(w io.Writer, path string) error {
	r, err := openRd(path)
	if err != nil {
		return err
	}
	defer r.Close()
	var walk func(start, end int64, depth int)
	walk = func(start, end int64, depth int) {
		for _, b := range children(r, start, end) {
			fmt.Fprintf(w, "%s%q @%d len=%d\n", strings.Repeat("  ", depth), b.typ, b.start, b.end-b.start)
			switch b.typ {
			case "moov", "trak", "mdia", "minf", "stbl", "udta", "edts", "dinf":
				walk(b.data, b.end, depth+1)
			case "meta":
				walk(b.data+4, b.end, depth+1)
			case "uuid":
				walk(b.data+16, b.end, depth+1)
			}
		}
	}
	walk(0, r.size, 0)
	return nil
}
