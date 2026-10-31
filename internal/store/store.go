// Package store keeps profiles, settings and run history as JSON files in
// the config folder (%APPDATA%\PhotoSorter on Windows, /config in Docker).
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"photosorter/internal/sorter"
)

type Profile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Source   string `json:"source"` // the profile's to-sort folder (optional on Windows)
	Dest     string `json:"dest"`
	Move     bool   `json:"move"`
	JPG      bool   `json:"jpg"`
	RAW      bool   `json:"raw"`
	MOV      bool   `json:"mov"`
	Rename   bool   `json:"rename"`
	Sidecars bool   `json:"sidecars"`
	Parallel int    `json:"parallel"` // files copied at once; 0 = automatic
}

// Options turns a profile into import options for the given sources.
func (p Profile) Options(sources []string) sorter.Options {
	return sorter.Options{Sources: sources, Dest: p.Dest, Move: p.Move, JPG: p.JPG, RAW: p.RAW, MOV: p.MOV, Rename: p.Rename, Sidecars: p.Sidecars, Parallel: p.Parallel}
}

// Settings remembers the last manual import.
type Settings struct {
	Last sorter.Options `json:"last"`
}

// HistoryEntry is one finished sort.
type HistoryEntry struct {
	ID      string          `json:"id"`
	Profile string          `json:"profile"` // profile name, or "Manual import"
	Options sorter.Options  `json:"options"`
	Summary sorter.Summary  `json:"summary"`
	Results []sorter.Result `json:"results,omitempty"`
}

type Store struct {
	dir string
	mu  sync.Mutex
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "history"), 0o777); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) read(name string, v any) error {
	b, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// write saves atomically (temp file, then rename) so a crash can't leave half a file.
func (s *Store) write(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(s.dir, name)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o666); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s *Store) Profiles() []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ps []Profile
	s.read("profiles.json", &ps)
	sort.SliceStable(ps, func(i, j int) bool { return strings.ToLower(ps[i].Name) < strings.ToLower(ps[j].Name) })
	return ps
}

func (s *Store) Profile(id string) (Profile, bool) {
	for _, p := range s.Profiles() {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// SaveProfile adds or replaces a profile (matched by ID).
func (s *Store) SaveProfile(p Profile) (Profile, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return p, errors.New("give the profile a name")
	}
	if strings.TrimSpace(p.Dest) == "" {
		return p, errors.New("choose a destination folder")
	}
	ps := s.Profiles()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range ps {
		if q.ID != p.ID && strings.EqualFold(q.Name, p.Name) {
			return p, errors.New("there's already a profile called " + q.Name)
		}
	}
	if p.ID == "" {
		p.ID = time.Now().Format("20060102150405.000")
		ps = append(ps, p)
	} else {
		found := false
		for i := range ps {
			if ps[i].ID == p.ID {
				ps[i], found = p, true
			}
		}
		if !found {
			ps = append(ps, p)
		}
	}
	return p, s.write("profiles.json", ps)
}

func (s *Store) DeleteProfile(id string) error {
	ps := s.Profiles()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := ps[:0]
	for _, p := range ps {
		if p.ID != id {
			out = append(out, p)
		}
	}
	return s.write("profiles.json", out)
}

func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Settings{Last: sorter.Options{JPG: true, RAW: true, MOV: true, Rename: true, Sidecars: true}}
	s.read("settings.json", &st)
	return st
}

func (s *Store) SaveSettings(st Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write("settings.json", st)
}

// AddHistory saves a run: a small summary file for the list and a full one.
func (s *Store) AddHistory(h HistoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.write(filepath.Join("history", h.ID+".json"), h); err != nil {
		return err
	}
	h.Results = nil
	return s.write(filepath.Join("history", h.ID+".summary.json"), h)
}

// History lists runs, newest first, without per-file results.
func (s *Store) History() []HistoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, _ := filepath.Glob(filepath.Join(s.dir, "history", "*.summary.json"))
	var out []HistoryEntry
	for _, f := range files {
		var h HistoryEntry
		if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &h) == nil {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Summary.Started.After(out[j].Summary.Started) })
	return out
}

func (s *Store) HistoryEntry(id string) (HistoryEntry, error) {
	var h HistoryEntry
	if strings.ContainsAny(id, `/\.`) {
		return h, errors.New("bad id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return h, s.read(filepath.Join("history", id+".json"), &h)
}

// Seen is the last job that handled a file, and what it did with it.
type Seen struct {
	HistoryID string    `json:"historyId"`
	Profile   string    `json:"profile"`
	Started   time.Time `json:"started"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
}

// RecentResults maps source paths to the newest of the recent jobs that
// handled them, so the Leftovers page can say which job left each file.
func (s *Store) RecentResults(limit int) map[string]Seen {
	out := map[string]Seen{}
	hs := s.History()
	if len(hs) > limit {
		hs = hs[:limit]
	}
	for i := len(hs) - 1; i >= 0; i-- { // newer jobs win
		full, err := s.HistoryEntry(hs[i].ID)
		if err != nil {
			continue
		}
		for _, r := range full.Results {
			out[r.Src] = Seen{HistoryID: full.ID, Profile: full.Profile, Started: full.Summary.Started, Status: r.Status, Error: r.Error}
		}
	}
	return out
}
