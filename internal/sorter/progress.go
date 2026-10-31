package sorter

import "sync"

// Progress is shared between a running scan or sort and the web UI.
type Progress struct {
	mu sync.Mutex
	s  ProgressSnapshot
}

type ProgressSnapshot struct {
	Phase       string         `json:"phase"`
	Done        int            `json:"done"`
	Total       int            `json:"total"`
	BytesDone   int64          `json:"bytesDone"`
	BytesTotal  int64          `json:"bytesTotal"`
	Current     string         `json:"current"`
	Source      int            `json:"source"` // 0-based index of the source being processed
	Sources     int            `json:"sources"`
	SourceDone  int            `json:"sourceDone"`
	SourceTotal int            `json:"sourceTotal"`
	Counts      map[string]int `json:"counts"`
	Workers     int            `json:"workers"` // files copied at the same time
	Active      []string       `json:"active"`  // file each worker is on
}

func NewProgress() *Progress { return &Progress{s: ProgressSnapshot{Counts: map[string]int{}}} }

func (p *Progress) Snapshot() ProgressSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.s
	s.Counts = make(map[string]int, len(p.s.Counts))
	for k, v := range p.s.Counts {
		s.Counts[k] = v
	}
	s.Active = nil
	for _, a := range p.s.Active {
		if a != "" {
			s.Active = append(s.Active, a)
		}
	}
	return s
}

func (p *Progress) update(fn func(s *ProgressSnapshot)) {
	p.mu.Lock()
	fn(&p.s)
	p.mu.Unlock()
}

func (p *Progress) phase(name string, total int) {
	p.update(func(s *ProgressSnapshot) { s.Phase, s.Done, s.Total, s.Current = name, 0, total, "" })
}

func (p *Progress) step(current string) {
	p.update(func(s *ProgressSnapshot) { s.Done++; s.Current = current })
}
