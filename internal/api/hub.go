// Package api wires the collectors together and serves the dashboard.
package api

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wow-look-at-my/sglang-dash/internal/cache"
	"github.com/wow-look-at-my/sglang-dash/internal/diagnose"
	"github.com/wow-look-at-my/sglang-dash/internal/events"
	"github.com/wow-look-at-my/sglang-dash/internal/metrics"
	"github.com/wow-look-at-my/sglang-dash/internal/requests"
)

// Hub owns the dashboard's state. It implements upstream.Recorder, so the
// proxy reports into it, and the HTTP handlers read out of it.
type Hub struct {
	Events   *events.Bus
	Metrics  *metrics.Store
	Cache    *cache.Tree
	Requests *requests.Store
	Diag     *diagnose.Diagnoser

	Mode string
	// Upstream is the server being observed, or "" in demo mode.
	Upstream string
	// StartedAt is when this dashboard process came up.
	StartedAt time.Time

	mu      sync.Mutex
	prompts map[string]string
}

// NewHub builds the hub.
func NewHub(bus *events.Bus, m *metrics.Store, c *cache.Tree, r *requests.Store, d *diagnose.Diagnoser, mode, upstream string) *Hub {
	return &Hub{
		Events: bus, Metrics: m, Cache: c, Requests: r, Diag: d,
		Mode: mode, Upstream: upstream, StartedAt: time.Now(),
		prompts: map[string]string{},
	}
}

// SignalsFrom reads the classification inputs straight off a metrics store.
// The scraper's own Signals adds restart detection, which needs scrape history.
func SignalsFrom(store *metrics.Store) cache.Signals {
	snap := store.Last()
	return cache.Signals{
		TokenUsage:      snap.Gauges["token_usage"],
		UsedTokens:      snap.Gauges["used_tokens"],
		RunningRequests: snap.Gauges["running_reqs"],
		MetricsOK:       snap.Ok,
	}
}

// Started records an arriving request and folds its prompt into the prefix
// tree. The cache observation happens here, not at completion, because the
// question the panel answers is "what is being ingested right now".
func (h *Hub) Started(r requests.Record, prompt string) requests.Record {
	snap := h.Metrics.Last()
	if snap.Ok {
		r.QueueDepthAtArrival = snap.Gauges["queued_reqs"]
		r.RunningAtArrival = snap.Gauges["running_reqs"]
		r.TokenUsageAtArrival = snap.Gauges["token_usage"]
	} else {
		r.QueueDepthAtArrival, r.RunningAtArrival, r.TokenUsageAtArrival = -1, -1, -1
	}

	if prompt != "" {
		h.mu.Lock()
		h.prompts[r.ID] = prompt
		h.mu.Unlock()
		m := h.Cache.Walk(prompt)
		r.CachePath = m.Path
		r.CacheMatch = m
		h.Cache.MarkInFlight(m.Path, 1)
	}

	h.Requests.Put(r)
	h.Events.Publish(events.KindRequestArrived, describeArrival(r), map[string]any{
		"request_id": r.ID, "path": r.Path, "model": r.Model, "stream": r.Stream,
		"prompt_chars": r.PromptChars, "queued": r.QueueDepthAtArrival, "running": r.RunningAtArrival,
	})
	return r
}

func describeArrival(r requests.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s prompt of %d chars", r.Path, r.PromptChars)
	if r.Model != "" {
		fmt.Fprintf(&b, " for %s", r.Model)
	}
	if r.CacheMatch.ExpectedCachedTokens > 0 {
		fmt.Fprintf(&b, "; the reconstructed tree already holds about %d of its prefix tokens", r.CacheMatch.ExpectedCachedTokens)
	}
	if r.QueueDepthAtArrival > 0 {
		fmt.Fprintf(&b, "; %.0f already queued", r.QueueDepthAtArrival)
	}
	return b.String()
}

// FirstToken stamps the TTFT on the live record.
func (h *Hub) FirstToken(id string, at time.Time) {
	r, ok := h.Requests.Get(id)
	if !ok {
		return
	}
	r.FirstTokenAt = at.UnixMilli()
	h.Requests.Put(r)
	h.Events.Publish(events.KindRequestFirstTok, fmt.Sprintf("first token after %dms", r.TTFTMs()),
		map[string]any{"request_id": id, "ttft_ms": r.TTFTMs()})
}

// Finished completes a record: it re-observes the prompt now that the server
// has reported real token counts, diagnoses the latency, and publishes both.
func (h *Hub) Finished(r requests.Record) {
	h.mu.Lock()
	prompt := h.prompts[r.ID]
	delete(h.prompts, r.ID)
	h.mu.Unlock()

	if len(r.CachePath) > 0 {
		h.Cache.MarkInFlight(r.CachePath, -1)
	}

	// The prompt is folded in whatever the server said about it: a request
	// whose usage block carried no cached_tokens still put its prefix in the
	// cache, and the next request needs the tree to know that.
	if prompt != "" {
		m, evictions := h.Cache.Observe(cache.Observation{
			RequestID: r.ID, Time: time.Now(), Prompt: prompt,
			PromptTokens: r.PromptTokens, CachedTokens: r.CachedTokens,
		})
		r.CachePath = m.Path
		r.CacheMatch = m
		h.publishCacheOutcome(r, m)
		for _, ev := range evictions {
			h.publishEviction(ev)
		}
	}

	h.Diag.Observe(r)
	if r.Status == requests.StatusDone || r.Status == requests.StatusFailed {
		d := h.Diag.Explain(r)
		r.Diagnosis = &d
	}
	h.Requests.Put(r)

	switch r.Status {
	case requests.StatusFailed:
		h.Events.Publish(events.KindRequestFailed, r.Error, map[string]any{"request_id": r.ID, "http": r.HTTPCode})
	case requests.StatusAborted:
		h.Events.Publish(events.KindRequestAborted, r.Error, map[string]any{"request_id": r.ID})
	default:
		h.Events.Publish(events.KindRequestCompleted, describeCompletion(r), map[string]any{
			"request_id": r.ID, "total_ms": r.TotalMs(), "ttft_ms": r.TTFTMs(),
			"prompt_tokens": r.PromptTokens, "cached_tokens": r.CachedTokens,
			"completion_tokens": r.CompletionTokens,
		})
	}
	if r.Diagnosis != nil && r.Diagnosis.Slow {
		h.Events.Publish(events.KindSlowDetected, describeSlow(r), map[string]any{
			"request_id": r.ID, "total_ms": r.TotalMs(), "headline": r.Diagnosis.Headline,
			"threshold_ms": r.Diagnosis.ThresholdMs,
		})
	}
}

func describeCompletion(r requests.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%dms total", r.TotalMs())
	if ttft := r.TTFTMs(); ttft >= 0 && r.Stream {
		fmt.Fprintf(&b, ", %dms to first token", ttft)
	}
	if r.CompletionTokens > 0 {
		fmt.Fprintf(&b, ", %d output tokens at %.0f tok/s", r.CompletionTokens, r.OutputTokensPerSec())
	}
	return b.String()
}

func describeSlow(r requests.Record) string {
	d := r.Diagnosis
	var b strings.Builder
	fmt.Fprintf(&b, "%dms, over the %dms threshold", r.TotalMs(), d.ThresholdMs)
	if len(d.Causes) > 0 {
		c := d.Causes[0]
		fmt.Fprintf(&b, "; the largest slice is %s at %dms (%.0f%%)", c.Factor, c.Ms, c.Share*100)
	}
	return b.String()
}

func (h *Hub) publishCacheOutcome(r requests.Record, m cache.Match) {
	fields := map[string]any{
		"request_id": r.ID, "cached_tokens": r.CachedTokens, "prompt_tokens": r.PromptTokens,
		"expected_cached_tokens": m.ExpectedCachedTokens, "reused_nodes": len(m.ReusedNodes),
		"new_nodes": len(m.NewNodes),
	}
	if r.CachedTokens < 0 {
		h.Events.Publish(events.KindCacheInserted,
			fmt.Sprintf("the response carried no cached_tokens figure, so the hit is unknown; %d new prefix chunks were inserted", len(m.NewNodes)), fields)
		return
	}
	if r.CachedTokens > 0 {
		pct := 0.0
		if r.PromptTokens > 0 {
			pct = float64(r.CachedTokens) / float64(r.PromptTokens) * 100
		}
		h.Events.Publish(events.KindCacheHit,
			fmt.Sprintf("%d of %d prompt tokens came from the prefix cache (%.0f%%)", r.CachedTokens, r.PromptTokens, pct), fields)
		return
	}
	h.Events.Publish(events.KindCacheMiss,
		fmt.Sprintf("none of the %d prompt tokens were cached; %d new prefix chunks were inserted", r.PromptTokens, len(m.NewNodes)), fields)
}

func (h *Hub) publishEviction(ev cache.Eviction) {
	h.Events.Publish(events.KindCacheEvicted,
		fmt.Sprintf("about %d cached prefix tokens are gone — %s (%s)", ev.Tokens, ev.Reason, ev.Confidence),
		map[string]any{
			"tokens": ev.Tokens, "reason": ev.Reason, "confidence": string(ev.Confidence),
			"nodes": len(ev.NodeIDs), "evidence": ev.Evidence, "request_id": ev.RequestID,
		})
}
