package upstream

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/wow-look-at-my/sglang-dash/internal/cache"
	"github.com/wow-look-at-my/sglang-dash/internal/events"
	"github.com/wow-look-at-my/sglang-dash/internal/metrics"
)

// Scraper polls the upstream Prometheus endpoint on a fixed cadence.
type Scraper struct {
	url    *url.URL
	every  time.Duration
	client *http.Client
	store  *metrics.Store
	bus    *events.Bus

	mu     sync.RWMutex
	lastOK bool
	// everFailed gates the recovery notice, so a healthy opening scrape is silent.
	everFailed bool
	restarted  bool
	counters   map[string]float64
	stallSince time.Time
	stalled    bool
	pressure   bool
}

// NewScraper builds a scraper. It does not start polling.
func NewScraper(u *url.URL, every time.Duration, store *metrics.Store, bus *events.Bus) *Scraper {
	return &Scraper{
		url: u, every: every, store: store, bus: bus,
		client:   &http.Client{Timeout: 5 * time.Second},
		counters: map[string]float64{},
	}
}

// Run polls until the context ends. A failed scrape retries on the same fixed cadence forever.
func (s *Scraper) Run(ctx context.Context) {
	t := time.NewTicker(s.every)
	defer t.Stop()
	s.scrapeOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.scrapeOnce(ctx)
		}
	}
}

func (s *Scraper) scrapeOnce(ctx context.Context) {
	now := time.Now().UnixMilli()
	samples, err := s.fetch(ctx)
	if err != nil {
		s.store.Fail(now, err)
		s.mu.Lock()
		wasOK := s.lastOK
		s.lastOK = false
		s.everFailed = true
		s.mu.Unlock()
		if wasOK {
			s.bus.Publish(events.KindUpstreamDown, "metrics scrape failed: "+err.Error(), map[string]any{"url": s.url.String()})
		}
		return
	}
	snap := s.store.Ingest(now, samples)

	s.mu.Lock()
	recovered := s.everFailed && !s.lastOK
	s.lastOK = true
	s.restarted = false
	for _, field := range []string{"requests_total", "prompt_tokens", "generation_tokens"} {
		v, ok := snap.Gauges[field]
		if !ok {
			continue
		}
		if prev, seen := s.counters[field]; seen && v < prev {
			s.restarted = true
		}
		s.counters[field] = v
	}
	restarted := s.restarted
	running := snap.Gauges["running_reqs"]
	throughput := snap.Gauges["gen_throughput"]
	stalled := running > 0 && throughput == 0
	wasStalled := s.stalled
	if stalled && s.stallSince.IsZero() {
		s.stallSince = time.Now()
	}
	stallFor := time.Duration(0)
	if stalled {
		stallFor = time.Since(s.stallSince)
	} else {
		s.stallSince = time.Time{}
	}
	confirmed := stalled && stallFor >= 3*s.every
	s.stalled = confirmed
	usage, haveUsage := snap.Gauges["token_usage"]
	// Pressure is edge-triggered with a gap, so a value on the line cannot flap.
	wasPressure := s.pressure
	if haveUsage {
		if usage >= 0.95 {
			s.pressure = true
		} else if usage < 0.85 {
			s.pressure = false
		}
	}
	nowPressure := s.pressure
	s.mu.Unlock()

	if recovered {
		s.bus.Publish(events.KindUpstreamUp, "metrics scrape recovered", map[string]any{"url": s.url.String()})
	}
	if restarted {
		s.bus.Publish(events.KindCacheFlushed, "an upstream counter went backwards, which means the server restarted and its prefix cache is empty", nil)
	}
	if confirmed && !wasStalled {
		s.bus.Publish(events.KindSchedStalled, fmt.Sprintf("%.0f requests have been running for %s with zero generation throughput", running, stallFor.Round(time.Second)),
			map[string]any{"running": running, "stalled_for_ms": stallFor.Milliseconds()})
	}
	if !confirmed && wasStalled {
		s.bus.Publish(events.KindSchedRecovered, "generation throughput resumed", nil)
	}
	if nowPressure && !wasPressure {
		s.bus.Publish(events.KindMemoryPressure, fmt.Sprintf("KV token usage reached %.0f%%; the server will start evicting cached prefixes to admit new work", usage*100),
			map[string]any{"token_usage": usage})
	}
	if wasPressure && !nowPressure {
		s.bus.Publish(events.KindSchedRecovered, fmt.Sprintf("KV token usage fell back to %.0f%%", usage*100), map[string]any{"token_usage": usage})
	}
}

func (s *Scraper) fetch(ctx context.Context) ([]metrics.Sample, error) {
	ctx, cancel := context.WithTimeout(ctx, s.client.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("%s returned %s: %s", s.url, resp.Status, string(body))
	}
	return metrics.Parse(resp.Body)
}

// Signals reports the numbers the cache model classifies an eviction against.
func (s *Scraper) Signals() cache.Signals {
	snap := s.store.Last()
	s.mu.RLock()
	restarted := s.restarted
	s.mu.RUnlock()
	return cache.Signals{
		TokenUsage:        snap.Gauges["token_usage"],
		UsedTokens:        snap.Gauges["used_tokens"],
		RunningRequests:   snap.Gauges["running_reqs"],
		UpstreamRestarted: restarted,
		MetricsOK:         snap.Ok,
	}
}
