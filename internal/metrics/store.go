package metrics

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Point is a single value of a single series at a single scrape.
type Point struct {
	Time  int64   `json:"t"`
	Value float64 `json:"v"`
}

// Series is a scraped metric's history.
type Series struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Help   string            `json:"help,omitempty"`
	Type   string            `json:"type,omitempty"`
	points []Point
	head   int
	n      int
}

// Snapshot is what the API hands the browser after a scrape.
type Snapshot struct {
	Time    int64              `json:"time"`
	Ok      bool               `json:"ok"`
	Error   string             `json:"error,omitempty"`
	Gauges  map[string]float64 `json:"gauges"`
	Missing []string           `json:"missing"`
	// Quantiles holds latency families the mapping recognised, in seconds.
	Quantiles map[string]Quantiles `json:"quantiles"`
}

// Quantiles are histogram estimates read off cumulative bucket counts.
type Quantiles struct {
	P50   float64 `json:"p50"`
	P90   float64 `json:"p90"`
	P99   float64 `json:"p99"`
	Count float64 `json:"count"`
	Sum   float64 `json:"sum"`
}

// Store keeps every series' recent history plus the latest snapshot.
type Store struct {
	mu       sync.RWMutex
	capacity int
	series   map[string]*Series
	last     Snapshot
}

// NewStore keeps `capacity` points per series.
func NewStore(capacity int) *Store {
	if capacity < 2 {
		capacity = 2
	}
	empty := Snapshot{Gauges: map[string]float64{}, Quantiles: map[string]Quantiles{}, Missing: []string{}}
	return &Store{capacity: capacity, series: map[string]*Series{}, last: empty}
}

// Ingest records a single scrape and returns the snapshot derived from it.
func (s *Store) Ingest(now int64, samples []Sample) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sample := range samples {
		key := sample.Key()
		ser, ok := s.series[key]
		if !ok {
			ser = &Series{Name: sample.Name, Labels: sample.Labels, Help: sample.Help, Type: sample.Type, points: make([]Point, s.capacity)}
			s.series[key] = ser
		}
		ser.points[ser.head] = Point{Time: now, Value: sample.Value}
		ser.head = (ser.head + 1) % len(ser.points)
		if ser.n < len(ser.points) {
			ser.n++
		}
	}
	snap := derive(now, samples)
	s.last = snap
	return snap
}

// Fail records a scrape that did not happen.
func (s *Store) Fail(now int64, err error) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{Time: now, Ok: false, Error: err.Error(), Gauges: map[string]float64{}, Quantiles: map[string]Quantiles{}, Missing: []string{}}
	s.last = snap
	return snap
}

// Last returns the most recent snapshot.
func (s *Store) Last() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.last
}

// History returns a series' points in scrape order. The bool reports
// whether the series has ever been scraped, which is not the same as it being empty.
func (s *Store) History(key string) ([]Point, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ser, ok := s.series[key]
	if !ok {
		return nil, false
	}
	out := make([]Point, 0, ser.n)
	start := (ser.head - ser.n + len(ser.points)) % len(ser.points)
	for i := 0; i < ser.n; i++ {
		out = append(out, ser.points[(start+i)%len(ser.points)])
	}
	return out, true
}

// Names lists every series key seen so far, sorted.
func (s *Store) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.series))
	for k := range s.series {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Gauges maps a dashboard field to the upstream metric names that can supply
// it, tried in order.
var Gauges = map[string][]string{
	"running_reqs":      {"sglang:num_running_reqs", "sglang:num_running_requests"},
	"queued_reqs":       {"sglang:num_queue_reqs", "sglang:num_waiting_requests"},
	"used_tokens":       {"sglang:num_used_tokens"},
	"token_usage":       {"sglang:token_usage"},
	"gen_throughput":    {"sglang:gen_throughput"},
	"cache_hit_rate":    {"sglang:cache_hit_rate"},
	"prompt_tokens":     {"sglang:prompt_tokens_total"},
	"generation_tokens": {"sglang:generation_tokens_total"},
	"requests_total":    {"sglang:num_requests_total"},
	"aborted_total":     {"sglang:num_aborted_requests_total"},
	"grammar_queue":     {"sglang:num_grammar_queue_reqs"},
	"spec_accept_len":   {"sglang:spec_accept_length"},
}

// Histograms maps a dashboard latency field to its upstream family names.
var Histograms = map[string][]string{
	"ttft":         {"sglang:time_to_first_token_seconds"},
	"tpot":         {"sglang:time_per_output_token_seconds", "sglang:inter_token_latency_seconds"},
	"e2e_latency":  {"sglang:e2e_request_latency_seconds"},
	"queue_time":   {"sglang:waiting_latency_seconds", "sglang:queue_time_seconds"},
	"prefill_time": {"sglang:prefill_latency_seconds"},
}

func derive(now int64, samples []Sample) Snapshot {
	byName := map[string][]Sample{}
	for _, s := range samples {
		byName[s.Name] = append(byName[s.Name], s)
	}
	snap := Snapshot{Time: now, Ok: true, Gauges: map[string]float64{}, Quantiles: map[string]Quantiles{}, Missing: []string{}}
	for field, candidates := range Gauges {
		found := false
		for _, name := range candidates {
			rows := byName[name]
			if len(rows) == 0 {
				continue
			}
			total := 0.0
			for _, r := range rows {
				total += r.Value
			}
			snap.Gauges[field] = total
			found = true
			break
		}
		if !found {
			snap.Missing = append(snap.Missing, field)
		}
	}
	for field, candidates := range Histograms {
		for _, name := range candidates {
			q, ok := quantilesFrom(byName, name)
			if !ok {
				continue
			}
			snap.Quantiles[field] = q
			break
		}
	}
	sort.Strings(snap.Missing)
	return snap
}

// quantilesFrom reads cumulative histogram buckets.
func quantilesFrom(byName map[string][]Sample, family string) (Quantiles, bool) {
	buckets := byName[family+"_bucket"]
	if len(buckets) == 0 {
		return Quantiles{}, false
	}
	type bucket struct {
		le    float64
		count float64
	}
	agg := map[float64]float64{}
	for _, b := range buckets {
		le, err := parseValue(b.Labels["le"])
		if err != nil {
			continue
		}
		agg[le] += b.Value
	}
	if len(agg) == 0 {
		return Quantiles{}, false
	}
	list := make([]bucket, 0, len(agg))
	for le, c := range agg {
		list = append(list, bucket{le, c})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].le < list[j].le })
	total := list[len(list)-1].count
	if total <= 0 {
		return Quantiles{Count: 0}, true
	}
	q := Quantiles{Count: total}
	for _, s := range byName[family+"_sum"] {
		q.Sum += s.Value
	}
	pick := func(p float64) float64 {
		target := p * total
		prevCount, prevLe := 0.0, 0.0
		for _, b := range list {
			if b.count >= target {
				if math.IsInf(b.le, 1) {
					return prevLe
				}
				span := b.count - prevCount
				if span <= 0 {
					return b.le
				}
				frac := (target - prevCount) / span
				return prevLe + frac*(b.le-prevLe)
			}
			prevCount, prevLe = b.count, b.le
		}
		return prevLe
	}
	q.P50, q.P90, q.P99 = pick(0.50), pick(0.90), pick(0.99)
	return q, true
}

// FormatKey builds the same key Sample.Key produces, for History lookups.
func FormatKey(name string, labels map[string]string) string {
	return Sample{Name: name, Labels: labels}.Key()
}

// ParseFloat is the parser the scraper uses for numbers inside JSON bodies.
func ParseFloat(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
