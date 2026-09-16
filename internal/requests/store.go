// Package requests records every request the dashboard proxies, with the
// timings and cache facts that explain how long it took.
package requests

import (
	"sort"
	"sync"

	"github.com/wow-look-at-my/go-containers/set"
	"github.com/wow-look-at-my/sglang-dash/internal/cache"
)

// Status is where a request got to.
type Status string

const (
	StatusInFlight Status = "in_flight"
	StatusDone     Status = "done"
	StatusFailed   Status = "failed"
	StatusAborted  Status = "aborted"
)

// Record is a single proxied request.
type Record struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Status   Status `json:"status"`
	HTTPCode int    `json:"httpCode,omitempty"`
	Error    string `json:"error,omitempty"`

	// All times are ms since epoch.
	ArrivedAt    int64 `json:"arrivedAt"`
	SentAt       int64 `json:"sentAt"`
	FirstTokenAt int64 `json:"firstTokenAt,omitempty"`
	FinishedAt   int64 `json:"finishedAt,omitempty"`

	PromptChars      int `json:"promptChars"`
	PromptTokens     int `json:"promptTokens"`
	CachedTokens     int `json:"cachedTokens"`
	CompletionTokens int `json:"completionTokens"`

	// PromptPreview is the head of the prompt, withheld when redacting.
	PromptPreview string `json:"promptPreview,omitempty"`
	// PromptTruncated reports that only the head of the body was inspected.
	PromptTruncated bool `json:"promptTruncated,omitempty"`

	// CachePath is the reconstructed prefix path this prompt walked.
	CachePath  []string    `json:"cachePath,omitempty"`
	CacheMatch cache.Match `json:"cacheMatch"`

	QueueDepthAtArrival float64 `json:"queueDepthAtArrival"`
	RunningAtArrival    float64 `json:"runningAtArrival"`
	TokenUsageAtArrival float64 `json:"tokenUsageAtArrival"`

	Diagnosis *Diagnosis `json:"diagnosis,omitempty"`
}

func (r Record) TTFTMs() int64 {
	if r.FirstTokenAt == 0 {
		return -1
	}
	return r.FirstTokenAt - r.ArrivedAt
}

func (r Record) DecodeMs() int64 {
	if r.FirstTokenAt == 0 || r.FinishedAt == 0 {
		return -1
	}
	return r.FinishedAt - r.FirstTokenAt
}

func (r Record) TotalMs() int64 {
	if r.FinishedAt == 0 {
		return -1
	}
	return r.FinishedAt - r.ArrivedAt
}

func (r Record) OutputTokensPerSec() float64 {
	d := r.DecodeMs()
	if d <= 0 || r.CompletionTokens <= 0 {
		return 0
	}
	return float64(r.CompletionTokens) / (float64(d) / 1000)
}

// Cause is a single contributor to a slow request.
type Cause struct {
	// Factor names the mechanism this time went into.
	Factor string `json:"factor"`
	// Ms is how much of the request's wall time this factor accounts for.
	Ms    int64   `json:"ms"`
	Share float64 `json:"share"`
	// Detail states the evidence in a single sentence.
	Detail string `json:"detail"`
	// Confidence is measured for a clock or usage reading, inferred when apportioned.
	Confidence cache.Confidence `json:"confidence"`
}

// Diagnosis answers "why did this request take that long".
type Diagnosis struct {
	// Slow reports whether the request crossed the slow threshold.
	Slow bool `json:"slow"`
	// ThresholdMs is the threshold it was compared against.
	ThresholdMs int64 `json:"thresholdMs"`
	// Headline is the single biggest factor's name, or "" when nothing stood out.
	Headline string   `json:"headline"`
	Causes   []Cause  `json:"causes"`
	Notes    []string `json:"notes,omitempty"`
}

// Store keeps the newest records, indexed by id.
type Store struct {
	mu      sync.RWMutex
	ring    []Record
	head    int
	n       int
	byID    map[string]int
	inFlgt  set.Set[string]
	total   int
	failed  int
	slowCnt int
}

// NewStore retains `capacity` records.
func NewStore(capacity int) *Store {
	if capacity < 16 {
		capacity = 16
	}
	return &Store{ring: make([]Record, capacity), byID: map[string]int{}, inFlgt: set.New[string]()}
}

// Put inserts or replaces a record.
func (s *Store) Put(r Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx, ok := s.byID[r.ID]; ok {
		s.ring[idx] = r
		s.trackLocked(r)
		return
	}
	idx := s.head
	if s.n == len(s.ring) {
		delete(s.byID, s.ring[idx].ID)
		s.inFlgt.Remove(s.ring[idx].ID)
	}
	s.ring[idx] = r
	s.byID[r.ID] = idx
	s.head = (s.head + 1) % len(s.ring)
	if s.n < len(s.ring) {
		s.n++
	}
	s.total++
	s.trackLocked(r)
}

func (s *Store) trackLocked(r Record) {
	if r.Status == StatusInFlight {
		s.inFlgt.Add(r.ID)
		return
	}
	s.inFlgt.Remove(r.ID)
	if r.Status == StatusFailed {
		s.failed++
	}
	if r.Diagnosis != nil && r.Diagnosis.Slow {
		s.slowCnt++
	}
}

// Get returns a single record.
func (s *Store) Get(id string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byID[id]
	if !ok {
		return Record{}, false
	}
	return s.ring[idx], true
}

// Recent returns the newest `limit` records, newest earliest.
func (s *Store) Recent(limit int) []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > s.n {
		limit = s.n
	}
	out := make([]Record, 0, s.n)
	start := (s.head - s.n + len(s.ring)) % len(s.ring)
	for i := 0; i < s.n; i++ {
		out = append(out, s.ring[(start+i)%len(s.ring)])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ArrivedAt > out[j].ArrivedAt })
	return out[:limit]
}

// Totals reports the counters the header strip shows.
type Totals struct {
	Seen     int `json:"seen"`
	Retained int `json:"retained"`
	InFlight int `json:"inFlight"`
	Failed   int `json:"failed"`
	Slow     int `json:"slow"`
}

// Totals returns the counters.
func (s *Store) Totals() Totals {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Totals{Seen: s.total, Retained: s.n, InFlight: s.inFlgt.Len(), Failed: s.failed, Slow: s.slowCnt}
}
