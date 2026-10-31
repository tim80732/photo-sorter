// Photo Sorter sorts camera files into <dest>\yyyymm\JPG|RAW|MOV.
//
//	photosorter                 Windows: open the app window. Linux: same as serve.
//	photosorter serve           run the web UI (Docker): --listen :8095 --config /config
//	photosorter plan ...        dry run (and optionally --run) from the command line, for testing
//	photosorter meta <paths>    print the capture date found for each file
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"photosorter/internal/server"
	"photosorter/internal/sorter"
	"photosorter/internal/store"
)

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "meta":
		os.Exit(cmdMeta(os.Stdout, os.Args[2:]))
	case "plan":
		os.Exit(cmdPlan(os.Args[2:]))
	case "serve":
		os.Exit(cmdServe(os.Args[2:]))
	case "":
		runDefault()
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", cmd)
		os.Exit(2)
	}
}

func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", envOr("PHOTOSORTER_LISTEN", ":8095"), "address to listen on")
	config := fs.String("config", envOr("PHOTOSORTER_CONFIG", "/config"), "folder for profiles and history")
	roots := fs.String("roots", envOr("PHOTOSORTER_ROOTS", "/camera-photo"), "comma-separated starting folders for the folder browser")
	fs.Parse(args)
	st, err := store.Open(*config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config folder:", err)
		return 1
	}
	var rootList []string
	for _, r := range strings.Split(*roots, ",") {
		if r = strings.TrimSpace(r); r != "" {
			rootList = append(rootList, r)
		}
	}
	srv := server.New(st, server.Host{Roots: func() []string { return rootList }})
	fmt.Printf("Photo Sorter %s listening on %s (config %s)\n", server.Version, *listen, *config)
	if err := http.ListenAndServe(*listen, srv.Handler()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// cmdPlan is the command-line version of scan -> preview -> run, used for
// automated tests. It writes the plan (and run results) as JSON to --out.
func cmdPlan(args []string) int {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	var srcs multiFlag
	fs.Var(&srcs, "src", "source folder (repeatable)")
	dest := fs.String("dest", "", "destination folder")
	move := fs.Bool("move", false, "move instead of copy")
	noRename := fs.Bool("no-rename", false, "keep file names as they are")
	noSide := fs.Bool("no-sidecars", false, "leave sidecars behind")
	noJPG := fs.Bool("no-jpg", false, "skip JPG/HEIC")
	noRAW := fs.Bool("no-raw", false, "skip RAW")
	noMOV := fs.Bool("no-mov", false, "skip videos")
	parallel := fs.Int("parallel", 0, "files copied at once (0 = auto)")
	run := fs.Bool("run", false, "carry out the plan after building it")
	out := fs.String("out", "", "write JSON here (default stdout)")
	only := fs.String("only", "", "text file listing the files to include, relative to the source (for benchmarks)")
	fs.Parse(args)
	opt := sorter.Options{Sources: srcs, Dest: *dest, Move: *move, JPG: !*noJPG, RAW: !*noRAW, MOV: !*noMOV, Rename: !*noRename, Sidecars: !*noSide, Parallel: *parallel}
	if *only != "" {
		b, err := os.ReadFile(*only)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		opt.Only = map[string]bool{}
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(strings.TrimPrefix(l, "\ufeff")); l != "" {
				opt.Only[strings.ToLower(l)] = true
			}
		}
	}
	prog := sorter.NewProgress()
	plan, err := sorter.BuildPlan(context.Background(), opt, prog)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	res := map[string]any{"plan": plan}
	if *run {
		sum, results := sorter.Run(context.Background(), plan, prog)
		res["summary"], res["results"] = sum, results
	}
	w := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	enc.Encode(res)
	return 0
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
