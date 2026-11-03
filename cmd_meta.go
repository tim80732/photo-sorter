package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"photosorter/internal/meta"
)

// cmdMeta prints the capture date the app would use for each file or for
// every file under each folder: photosorter meta <path>...
func cmdMeta(w io.Writer, args []string) int {
	if len(args) > 1 && args[0] == "--boxes" {
		for _, a := range args[1:] {
			if err := meta.DumpBoxes(w, a); err != nil {
				fmt.Fprintln(w, err)
				return 1
			}
		}
		return 0
	}
	for _, a := range args {
		filepath.WalkDir(a, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				fmt.Fprintln(os.Stderr, "skipped:", err)
				return nil
			}
			if d.IsDir() {
				return nil
			}
			st, err := d.Info()
			if err != nil {
				fmt.Fprintln(os.Stderr, "skipped:", err)
				return nil
			}
			r := meta.Read(p, st.ModTime())
			fb := ""
			if r.Fallback {
				fb = "  [fallback]"
			}
			fmt.Fprintf(w, "%s  %-28s %s%s\n", r.Time.Format("2006-01-02 15:04:05"), r.Source, p, fb)
			return nil
		})
	}
	return 0
}
