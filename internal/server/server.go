// Package server is the web UI and JSON API shared by the Windows app and
// the Docker container.
package server

import (
	"compress/gzip"
	"context"
	"embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"photosorter/internal/sorter"
	"photosorter/internal/store"
)

//go:embed static
var static embed.FS

// Version is set at build time.
var Version = "dev"

// Host is what the platform provides beyond the shared UI.
type Host struct {
	PickFolder func(start string) (string, error) // native folder dialog; nil in Docker
	Open       func(path string) error            // show a folder in Explorer; nil in Docker
	Quit       func()                             // nil unless the UI may close the app
	Roots      func() []string                    // starting points for the built-in folder browser
	LocalOnly  bool                               // require a localhost Host header (Windows)
}

type Server struct {
	st   *store.Store
	host Host

	mu    sync.Mutex
	jobs  map[string]*job
	plans map[string]*planEntry
	order []string // leftovers plan IDs, oldest first
}

type planEntry struct {
	plan    *sorter.Plan
	profile string // profile name or "Manual import"
	purpose string // "import" or "leftovers"
	deleted map[int]bool
	runID   string // set once approved: the sort job running it
}

// A job is a scan or a sort, and several can run at once. An import shows on
// the Active jobs tab as its scan, then its preview, then its sort.
type job struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"` // scan, run
	Done      bool            `json:"done"`
	Error     string          `json:"error,omitempty"`
	PlanID    string          `json:"planId,omitempty"` // a scan's result, or the plan a sort runs
	RunID     string          `json:"runId,omitempty"`  // a scan whose preview was approved: its sort
	HistoryID string          `json:"historyId,omitempty"`
	Summary   *sorter.Summary `json:"summary,omitempty"`
	Purpose   string          `json:"purpose,omitempty"`
	Profile   string          `json:"profile"`
	Sources   []string        `json:"sources"`
	Dest      string          `json:"dest"`
	Move      bool            `json:"move"`
	Quick     bool            `json:"quickCheck"`
	Tidy      bool            `json:"tidy,omitempty"` // a Rename job
	Started   time.Time       `json:"started"`

	ended   bool            // a stopped sort that won't be continued
	results []sorter.Result // a sort's results so far, across continues
	prog    *sorter.Progress
	cancel  context.CancelFunc
}

// HTTPServer wraps the handler with timeouts that drop clients that never
// finish sending a request. There's no write timeout: a big preview or CSV
// on a slow link may take a while.
func (s *Server) HTTPServer() *http.Server {
	return &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
}

func New(st *store.Store, host Host) *Server {
	return &Server{st: st, host: host, jobs: map[string]*job{}, plans: map[string]*planEntry{}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))

	mux.HandleFunc("GET /api/info", s.info)
	mux.HandleFunc("GET /api/profiles", s.profiles)
	mux.HandleFunc("POST /api/profiles", s.saveProfile)
	mux.HandleFunc("DELETE /api/profiles/{id}", s.deleteProfile)
	mux.HandleFunc("GET /api/count", s.count)
	mux.HandleFunc("GET /api/settings", s.settings)
	mux.HandleFunc("GET /api/browse", s.browse)
	mux.HandleFunc("POST /api/pick", s.pick)
	mux.HandleFunc("POST /api/mkdir", s.mkdir)
	mux.HandleFunc("POST /api/open", s.open)
	mux.HandleFunc("POST /api/quit", s.quit)
	mux.HandleFunc("POST /api/scan", s.scan)
	mux.HandleFunc("GET /api/jobs", s.listJobs)
	mux.HandleFunc("POST /api/jobs/{id}/continue", s.continueJob)
	mux.HandleFunc("POST /api/jobs/{id}/end", s.endJob)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.cancelJob)
	mux.HandleFunc("GET /api/plans/{id}", s.getPlan)
	mux.HandleFunc("GET /api/plans/{id}/csv", s.planCSV)
	mux.HandleFunc("POST /api/plans/{id}/run", s.runPlan)
	mux.HandleFunc("DELETE /api/plans/{id}", s.discardPlan)
	mux.HandleFunc("POST /api/plans/{id}/delete", s.deleteItems)
	mux.HandleFunc("GET /api/history", s.history)
	mux.HandleFunc("GET /api/history/{id}", s.historyEntry)
	return s.guard(mux)
}

// guard blocks cross-site requests: changes need a custom header that other
// sites can't send, and the Windows app only answers to localhost.
func (s *Server) guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.host.LocalOnly {
			host, _, _ := net.SplitHostPort(r.Host)
			if host != "127.0.0.1" && host != "localhost" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		if r.Method != http.MethodGet && r.Header.Get("X-PhotoSorter") != "1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		json.NewEncoder(gz).Encode(v)
		return
	}
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 32<<20)).Decode(v)
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	roots := []string{}
	if s.host.Roots != nil {
		roots = s.host.Roots()
	}
	writeJSON(w, r, map[string]any{
		"version":  Version,
		"platform": runtime.GOOS,
		"native":   s.host.PickFolder != nil,
		"canOpen":  s.host.Open != nil,
		"canQuit":  s.host.Quit != nil,
		"roots":    roots,
		"config":   s.st.Dir(),
	})
}

func (s *Server) profiles(w http.ResponseWriter, r *http.Request) {
	ps := s.st.Profiles()
	if ps == nil {
		ps = []store.Profile{}
	}
	writeJSON(w, r, ps)
}

func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request) {
	var p store.Profile
	if err := readJSON(r, &p); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := sorter.CheckPattern(p.Pattern); err != nil {
		fail(w, 400, err.Error())
		return
	}
	p, err := s.st.SaveProfile(p)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, r, p)
}

func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteProfile(r.PathValue("id")); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, r, map[string]bool{"ok": true})
}

// count quickly tallies a folder for the home page (no metadata reading).
func (s *Server) count(w http.ResponseWriter, r *http.Request) {
	root := r.URL.Query().Get("path")
	res := map[string]any{"exists": false, "media": 0, "other": 0, "bytes": int64(0)}
	st, err := os.Stat(root)
	if root == "" || err != nil || !st.IsDir() {
		writeJSON(w, r, res)
		return
	}
	res["exists"] = true
	media, other := 0, 0
	var bytes int64
	deadline := time.Now().Add(20 * time.Second)
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if time.Now().After(deadline) {
			res["partial"] = true
			return filepath.SkipAll
		}
		if d.IsDir() {
			if p != root && sorter.SkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if sorter.IsMediaName(d.Name()) {
			media++
			if info, err := d.Info(); err == nil {
				bytes += info.Size()
			}
		} else {
			other++
		}
		return nil
	})
	res["media"], res["other"], res["bytes"] = media, other, bytes
	writeJSON(w, r, res)
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, s.st.Settings())
}

func (s *Server) browse(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	roots := []string{}
	if s.host.Roots != nil {
		roots = s.host.Roots()
	}
	if p == "" {
		writeJSON(w, r, map[string]any{"path": "", "parent": "", "dirs": roots, "roots": roots})
		return
	}
	p = filepath.Clean(p)
	ents, err := os.ReadDir(p)
	if err != nil {
		fail(w, 400, "Can't open that folder: "+err.Error())
		return
	}
	dirs := []string{}
	for _, e := range ents {
		if e.IsDir() && !sorter.SkipDir(e.Name()) && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, filepath.Join(p, e.Name()))
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i]) < strings.ToLower(dirs[j]) })
	parent := filepath.Dir(p)
	if parent == p {
		parent = ""
	}
	writeJSON(w, r, map[string]any{"path": p, "parent": parent, "dirs": dirs, "roots": roots})
}

func (s *Server) pick(w http.ResponseWriter, r *http.Request) {
	if s.host.PickFolder == nil {
		fail(w, 400, "not available")
		return
	}
	var req struct{ Start string }
	readJSON(r, &req)
	p, err := s.host.PickFolder(req.Start)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, r, map[string]string{"path": p})
}

func (s *Server) mkdir(w http.ResponseWriter, r *http.Request) {
	var req struct{ Path string }
	if err := readJSON(r, &req); err != nil || req.Path == "" {
		fail(w, 400, "no folder given")
		return
	}
	if err := os.MkdirAll(req.Path, 0o777); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, r, map[string]bool{"ok": true})
}

func (s *Server) open(w http.ResponseWriter, r *http.Request) {
	var req struct{ Path string }
	readJSON(r, &req)
	if s.host.Open == nil || req.Path == "" {
		fail(w, 400, "not available")
		return
	}
	if err := s.host.Open(req.Path); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, r, map[string]bool{"ok": true})
}

func (s *Server) quit(w http.ResponseWriter, r *http.Request) {
	if s.host.Quit == nil {
		fail(w, 400, "not available")
		return
	}
	writeJSON(w, r, map[string]bool{"ok": true})
	go func() { time.Sleep(200 * time.Millisecond); s.host.Quit() }()
}

// Status is the current scan or sort, for the Windows taskbar.
type Status struct {
	Running  bool
	Kind     string // "scan" or "run"
	Progress sorter.ProgressSnapshot
}

// Status sums up the running jobs: all sorts together as one bar, else
// "scan" while only scans run.
func (s *Server) Status() Status {
	s.mu.Lock()
	var running []*job
	for _, j := range s.jobs {
		if !j.Done {
			running = append(running, j)
		}
	}
	s.mu.Unlock()
	st := Status{Progress: sorter.ProgressSnapshot{Counts: map[string]int{}}}
	for _, j := range running {
		st.Running = true
		if st.Kind == "" {
			st.Kind = "scan"
		}
		if j.Kind != "run" {
			continue
		}
		st.Kind = "run"
		p := j.prog.Snapshot()
		st.Progress.BytesDone += p.BytesDone
		st.Progress.BytesTotal += p.BytesTotal
		st.Progress.Done += p.Done
		st.Progress.Total += p.Total
		st.Progress.Counts["failed"] += p.Counts["failed"]
	}
	return st
}

// StopAndWait cancels the running scans and sorts and waits for them to
// stop cleanly (a sort finishes or abandons the current file first).
func (s *Server) StopAndWait(timeout time.Duration) {
	s.mu.Lock()
	for _, j := range s.jobs {
		j.cancel()
	}
	s.mu.Unlock()
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if !s.busy("") {
			return
		}
	}
}

// busy reports whether a job of that kind ("" for any) is running.
func (s *Server) busy(kind string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if !j.Done && (kind == "" || j.Kind == kind) {
			return true
		}
	}
	return false
}

// startJob runs fn in the background, alongside any other jobs.
func (s *Server) startJob(kind, purpose string, opt sorter.Options, profile string, fn func(ctx context.Context, j *job)) (*job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := &job{ID: strconv.FormatInt(time.Now().UnixNano(), 36), Kind: kind, Purpose: purpose, Profile: profile,
		Sources: opt.Sources, Dest: opt.Dest, Move: opt.Move, Quick: opt.QuickCheck, Tidy: opt.Tidy, Started: time.Now(), prog: sorter.NewProgress()}
	s.jobs[j.ID] = j
	logJob(j, "started")
	s.launch(j, fn)
	return j, nil
}

// launch runs fn for j in the background: a new job, or a stopped sort being
// continued. Call it with s.mu held.
func (s *Server) launch(j *job, fn func(ctx context.Context, j *job)) {
	ctx, cancel := context.WithCancel(context.Background())
	j.cancel, j.Done = cancel, false
	go func() {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("%s %s: internal error: %v\n%s", j.Kind, j.Profile, v, debug.Stack())
				s.mu.Lock()
				j.Error = fmt.Sprint("internal error: ", v)
				j.Done = true
				s.mu.Unlock()
			}
		}()
		fn(ctx, j)
		s.mu.Lock()
		j.Done = true
		s.mu.Unlock()
		cancel()
	}()
}

// The log (docker logs on Unraid) records each job and every file a job
// failed on or deleted, so a sort nobody was watching can be checked later.
func logJob(j *job, what string) {
	kind := map[string]string{"run": "sort"}[j.Kind]
	if kind == "" {
		kind = j.Kind
	}
	if what == "started" {
		what += fmt.Sprintf(" (%s -> %s)", strings.Join(j.Sources, ", "), j.Dest)
	}
	log.Printf("%s: %s %s", j.Profile, kind, what)
}

func gb(n int64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }

type scanReq struct {
	Options   sorter.Options `json:"options"`
	ProfileID string         `json:"profileId"`
	Purpose   string         `json:"purpose"` // "import", "rename" or "leftovers"
	// Names, if set, is this import's file name, instead of the profile's.
	Names *struct {
		Rename  bool   `json:"rename"`
		Pattern string `json:"pattern"`
	} `json:"names,omitempty"`
}

func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	var req scanReq
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	opt := req.Options
	opt.Tidy = req.Purpose == "rename"
	if req.Names != nil {
		if err := sorter.CheckPattern(req.Names.Pattern); err != nil {
			fail(w, 400, err.Error())
			return
		}
	}
	if err := sorter.CheckPattern(opt.Pattern); err != nil {
		fail(w, 400, err.Error())
		return
	}
	name := "Manual import"
	if opt.Tidy {
		// Rename tidies one folder in place; profiles don't apply.
		name = "Rename"
		opt.Sources = []string{opt.Dest}
	} else if req.ProfileID != "" {
		p, ok := s.st.Profile(req.ProfileID)
		if !ok {
			fail(w, 404, "profile not found")
			return
		}
		name = p.Name
		srcs := opt.Sources
		if len(srcs) == 0 && p.Source != "" {
			srcs = []string{p.Source}
		}
		opt = p.Options(srcs)
		if req.Names != nil {
			opt.Rename, opt.Pattern = req.Names.Rename, req.Names.Pattern
		}
	} else if req.Purpose == "import" || req.Purpose == "" {
		st := s.st.Settings()
		st.Last = opt
		s.st.SaveSettings(st)
	}
	for _, p := range s.st.Profiles() {
		if p.Source != "" {
			opt.Protected = append(opt.Protected, p.Source)
		}
	}
	purpose := req.Purpose
	if purpose == "" {
		purpose = "import"
	}
	j, err := s.startJob("scan", purpose, opt, name, func(ctx context.Context, j *job) {
		plan, err := sorter.BuildPlan(ctx, opt, j.prog)
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			if ctx.Err() != nil {
				j.Error = "Scan cancelled"
				logJob(j, "cancelled")
			} else {
				j.Error = err.Error()
				logJob(j, "failed: "+j.Error)
			}
			return
		}
		n, b := plan.ToCopy()
		logJob(j, fmt.Sprintf("finished: %d files to sort (%s)", n, gb(b)))
		s.plans[plan.ID] = &planEntry{plan: plan, profile: name, purpose: purpose, deleted: map[int]bool{}}
		// Import previews stay until they're sorted or discarded. Leftovers
		// scans, and scans that found nothing to copy, are dropped oldest
		// first to keep memory bounded.
		if n, _ := plan.ToCopy(); purpose == "leftovers" || n == 0 {
			s.order = append(s.order, plan.ID)
			for len(s.order) > 4 {
				delete(s.plans, s.order[0])
				s.order = s.order[1:]
			}
		}
		j.PlanID = plan.ID
	})
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, r, j)
}

// stage is where a job stands. Active jobs lists the active stages
// (scanning, review, sorting, stopped); the rest are finished: a scan that
// found nothing to copy ("empty"), was cancelled or failed, a scan whose
// preview became a sort or was discarded (""), and finished sorts. Call it
// with s.mu held.
func (s *Server) stage(j *job) string {
	if j.Kind == "run" {
		_, resumable := s.plans[j.PlanID]
		switch {
		case !j.Done:
			return "sorting"
		case j.Summary == nil:
			return "failed"
		case j.Summary.Cancelled && resumable && !j.ended:
			return "stopped"
		case j.Summary.Cancelled:
			return "cancelled"
		case j.Summary.Failed > 0 && j.Summary.Copied+j.Summary.Moved == 0:
			return "failed"
		case j.Summary.Failed > 0:
			return "problems"
		}
		return "done"
	}
	switch {
	case !j.Done:
		return "scanning"
	case j.Error == "Scan cancelled":
		return "cancelled"
	case j.Error != "":
		return "failed"
	}
	if e, ok := s.plans[j.PlanID]; ok && j.RunID == "" {
		if n, _ := e.plan.ToCopy(); n == 0 {
			return "empty"
		}
		return "review"
	}
	return ""
}

func active(stage string) bool {
	return stage == "scanning" || stage == "review" || stage == "sorting" || stage == "stopped"
}

// jobJSON is a job with its live progress. Call it with s.mu held.
func (s *Server) jobJSON(j *job) map[string]any {
	var out map[string]any
	b, _ := json.Marshal(j)
	json.Unmarshal(b, &out)
	out["progress"] = j.prog.Snapshot()
	out["stage"] = s.stage(j)
	if e, ok := s.plans[j.PlanID]; ok {
		out["toCopy"], out["toCopyBytes"] = e.plan.ToCopy()
	}
	return out
}

// listJobs is the Active jobs tab: imports being scanned, waiting for
// approval, sorting, or stopped. Finished jobs are in Job history.
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	out := []map[string]any{}
	var js []*job
	for _, j := range s.jobs {
		if j.Purpose != "leftovers" && active(s.stage(j)) {
			js = append(js, j)
		}
	}
	sort.Slice(js, func(a, b int) bool { return js[a].Started.Before(js[b].Started) })
	for _, j := range js {
		out = append(out, s.jobJSON(j))
	}
	s.mu.Unlock()
	writeJSON(w, r, out)
}

// continueJob carries on with a stopped sort: only the files it hadn't
// reached are done, and its Job history entry is updated.
func (s *Server) continueJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[r.PathValue("id")]
	if !ok || s.stage(j) != "stopped" {
		fail(w, 409, "This job can't be continued.")
		return
	}
	for _, o := range s.jobs {
		if o != j && o.Kind == "run" && !o.Done && clashes(o, j.Sources, j.Dest, j.Tidy) {
			fail(w, 409, "Another job is sorting the same folder. Continue this one when it's finished.")
			return
		}
	}
	logJob(j, "continued")
	s.launch(j, s.sortFn(s.plans[j.PlanID]))
	writeJSON(w, r, s.jobJSON(j))
}

// endJob ends a stopped sort for good: the files it didn't reach stay where
// they are, and Job history shows it as cancelled.
func (s *Server) endJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[r.PathValue("id")]
	if ok && s.stage(j) == "stopped" {
		j.ended = true
		delete(s.plans, j.PlanID)
	}
	writeJSON(w, r, map[string]bool{"ok": ok})
}

// sortFn sorts a plan for a run job, or continues a stopped one, and keeps
// the job's single Job history entry up to date.
func (s *Server) sortFn(e *planEntry) func(ctx context.Context, j *job) {
	return func(ctx context.Context, j *job) {
		sum, results := sorter.Run(ctx, e.plan, j.prog)
		for _, res := range results {
			if res.Status == sorter.StFailed {
				log.Printf("  failed: %s: %s", res.Src, res.Error)
			}
		}
		what := "finished"
		if sum.Cancelled {
			what = "stopped"
		}
		logJob(j, fmt.Sprintf("%s: %d copied, %d moved (%s), %d duplicates, %d failed, %d not done", what, sum.Copied, sum.Moved, gb(sum.Bytes), sum.Duplicates, sum.Failed, sum.NotRun))
		s.mu.Lock()
		total := mergeSummary(j.Summary, sum)
		j.results = mergeResults(j.results, results)
		if j.HistoryID == "" {
			j.HistoryID = time.Now().Format("20060102-150405") + "-" + e.plan.ID[:4]
		}
		h := store.HistoryEntry{ID: j.HistoryID, Profile: e.profile, Options: e.plan.Options, Summary: total, Results: j.results}
		s.mu.Unlock()
		err := s.st.AddHistory(h)
		s.mu.Lock()
		defer s.mu.Unlock()
		j.Summary = &total
		if err != nil {
			j.Error = "Sorted, but the history couldn't be saved: " + err.Error()
			log.Print(j.Error)
		}
		// A finished plan is spent; forget it so it can't be run twice. A
		// stopped one is kept so the sort can be continued.
		if !sum.Cancelled {
			delete(s.plans, e.plan.ID)
		}
	}
}

// mergeSummary adds a continued run's numbers to the job's so far.
func mergeSummary(prev *sorter.Summary, cur sorter.Summary) sorter.Summary {
	if prev == nil {
		return cur
	}
	m := *prev
	m.Finished, m.Cancelled, m.NotRun = cur.Finished, cur.Cancelled, cur.NotRun
	m.Copied += cur.Copied
	m.Moved += cur.Moved
	m.Duplicates += cur.Duplicates
	m.Failed += cur.Failed
	m.Renamed += cur.Renamed
	m.Bytes += cur.Bytes
	m.CopiedBytes += cur.CopiedBytes
	m.Elapsed += cur.Elapsed
	m.Folders = append(m.Folders, cur.Folders...)
	return m
}

// mergeResults replaces earlier "not run" results with what a continued run did.
func mergeResults(prev, cur []sorter.Result) []sorter.Result {
	if prev == nil {
		return cur
	}
	at := map[int]int{}
	for i, r := range prev {
		at[r.ID] = i
	}
	for _, r := range cur {
		if i, ok := at[r.ID]; ok {
			prev[i] = r
		} else {
			prev = append(prev, r)
		}
	}
	sort.Slice(prev, func(a, b int) bool { return prev[a].ID < prev[b].ID })
	return prev
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	j, ok := s.jobs[r.PathValue("id")]
	var out map[string]any
	if ok {
		out = s.jobJSON(j)
	}
	s.mu.Unlock()
	if !ok {
		fail(w, 404, "no such job")
		return
	}
	writeJSON(w, r, out)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	j, ok := s.jobs[r.PathValue("id")]
	s.mu.Unlock()
	if ok {
		j.cancel()
	}
	writeJSON(w, r, map[string]bool{"ok": ok})
}

func (s *Server) plan(id string) (*planEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.plans[id]
	return e, ok
}

func (s *Server) getPlan(w http.ResponseWriter, r *http.Request) {
	e, ok := s.plan(r.PathValue("id"))
	if !ok {
		fail(w, 404, "This preview has expired. Scan again.")
		return
	}
	seen := map[string]store.Seen{}
	if e.purpose == "leftovers" {
		seen = s.st.RecentResults(30)
	}
	type itemOut struct {
		*sorter.Item
		Deleted bool        `json:"deleted,omitempty"`
		Failed  string      `json:"failed,omitempty"`
		LastJob *store.Seen `json:"lastJob,omitempty"` // the job that left it here
	}
	items := make([]itemOut, 0, len(e.plan.Items))
	for _, it := range e.plan.Items {
		o := itemOut{Item: it, Deleted: e.deleted[it.ID]}
		if sn, ok := seen[it.Src]; ok {
			o.LastJob = &sn
			if sn.Status == sorter.StFailed && it.Action == sorter.ActCopy {
				o.Failed = sn.Error
			}
		}
		items = append(items, o)
	}
	workers, why := sorter.Speed(e.plan.Options)
	out := map[string]any{
		"id": e.plan.ID, "created": e.plan.Created, "options": e.plan.Options,
		"profile": e.profile, "purpose": e.purpose, "tree": e.plan.Tree, "items": items,
		"workers": workers, "workersWhy": why,
	}
	if e.plan.Tidy != nil {
		out["tidy"] = e.plan.Tidy
	}
	if e.purpose != "leftovers" {
		if sp, ok := sorter.SpaceFor(e.plan); ok {
			out["space"] = sp
		}
	}
	writeJSON(w, r, out)
}

func (s *Server) planCSV(w http.ResponseWriter, r *http.Request) {
	e, ok := s.plan(r.PathValue("id"))
	if !ok {
		fail(w, 404, "This preview has expired. Scan again.")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="photo-sorter-preview.csv"`)
	w.Write([]byte("\xef\xbb\xbf")) // BOM so Excel reads UTF-8 names
	c := csv.NewWriter(w)
	c.Write([]string{"source", "file", "action", "reason", "destination", "matches", "capture date", "date from", "size", "note"})
	for _, it := range e.plan.Items {
		c.Write([]string{e.plan.Options.Sources[it.Source], it.Rel, it.Action, it.Reason, it.Dest, it.Match, it.Date, it.DateSrc, strconv.FormatInt(it.Size, 10), it.Note})
	}
	c.Flush()
}

func (s *Server) runPlan(w http.ResponseWriter, r *http.Request) {
	e, ok := s.plan(r.PathValue("id"))
	if !ok {
		fail(w, 404, "This preview has expired. Scan again.")
		return
	}
	if sp, ok := sorter.SpaceFor(e.plan); ok && !sp.Fits() {
		log.Printf("%s: sort refused: not enough space (needs %s, %s free on %s)", e.profile, gb(sp.Needed), gb(int64(sp.Free)), e.plan.Options.Dest)
		fail(w, 409, "Not enough space")
		return
	}
	// Two sorts of the same folder at once would fight over its files.
	s.mu.Lock()
	if e.runID != "" {
		s.mu.Unlock()
		fail(w, 409, "This preview was already approved.")
		return
	}
	for _, o := range s.jobs {
		if o.Kind == "run" && !o.Done && clashes(o, e.plan.Options.Sources, e.plan.Options.Dest, e.plan.Options.Tidy) {
			s.mu.Unlock()
			fail(w, 409, "Another job is sorting the same folder. Approve this one when it's finished.")
			return
		}
	}
	e.runID = "starting"
	s.mu.Unlock()
	j, err := s.startJob("run", e.purpose, e.plan.Options, e.profile, s.sortFn(e))
	if err != nil {
		s.mu.Lock()
		e.runID = ""
		s.mu.Unlock()
		fail(w, 409, err.Error())
		return
	}
	s.mu.Lock()
	e.runID = j.ID
	j.PlanID = e.plan.ID
	for _, sc := range s.jobs {
		if sc.Kind == "scan" && sc.PlanID == e.plan.ID {
			sc.RunID = j.ID
		}
	}
	s.mu.Unlock()
	writeJSON(w, r, j)
}

// discardPlan drops a preview that won't be approved.
func (s *Server) discardPlan(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	if e, ok := s.plans[id]; ok && e.runID == "" {
		delete(s.plans, id)
	}
	writeJSON(w, r, map[string]bool{"ok": true})
}

// clashes reports whether a running sort o would fight over files with a new
// one. Two imports clash only on their sources (they may share an archive);
// a Rename rearranges its whole folder, so it clashes with any job that reads
// or writes there.
func clashes(o *job, sources []string, dest string, tidy bool) bool {
	if !o.Tidy && !tidy {
		return overlaps(o.Sources, sources)
	}
	return overlaps(append(append([]string{}, o.Sources...), o.Dest), append(append([]string{}, sources...), dest))
}

// overlaps reports whether any folder in a is the same as, inside, or
// contains one in b.
func overlaps(a, b []string) bool {
	norm := func(p string) string {
		p, _ = filepath.Abs(p)
		p = strings.TrimRight(filepath.Clean(p), `/\`) + string(filepath.Separator)
		if runtime.GOOS == "windows" {
			p = strings.ToLower(p)
		}
		return p
	}
	for _, x := range a {
		for _, y := range b {
			nx, ny := norm(x), norm(y)
			if strings.HasPrefix(nx, ny) || strings.HasPrefix(ny, nx) {
				return true
			}
		}
	}
	return false
}

func (s *Server) deleteItems(w http.ResponseWriter, r *http.Request) {
	e, ok := s.plan(r.PathValue("id"))
	if !ok {
		fail(w, 404, "This scan has expired. Rescan.")
		return
	}
	var req struct{ IDs []int }
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.mu.Lock()
	for _, o := range s.jobs {
		if !o.Done && overlaps(o.Sources, e.plan.Options.Sources) {
			s.mu.Unlock()
			fail(w, 409, "A job is scanning or sorting this folder. Wait for it to finish.")
			return
		}
	}
	s.mu.Unlock()
	res := sorter.DeleteItems(e.plan, req.IDs)
	s.mu.Lock()
	for _, d := range res {
		if d.OK {
			e.deleted[d.ID] = true
			log.Printf("deleted: %s", d.Src)
		} else {
			log.Printf("couldn't delete: %s: %s", d.Src, d.Error)
		}
	}
	s.mu.Unlock()
	writeJSON(w, r, res)
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	h := s.st.History()
	if h == nil {
		h = []store.HistoryEntry{}
	}
	writeJSON(w, r, h)
}

func (s *Server) historyEntry(w http.ResponseWriter, r *http.Request) {
	h, err := s.st.HistoryEntry(r.PathValue("id"))
	if err != nil {
		fail(w, 404, "not found")
		return
	}
	writeJSON(w, r, h)
}
