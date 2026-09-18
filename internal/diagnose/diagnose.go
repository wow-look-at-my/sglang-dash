// Package diagnose attributes a request's wall time to the mechanisms that
// consumed it. It gives "why was that request slow" an answer with numbers
// behind it.
package diagnose

import (
	"fmt"
	"sort"
	"sync"

	"github.com/wow-look-at-my/sglang-dash/internal/cache"
	"github.com/wow-look-at-my/sglang-dash/internal/requests"
)

// Baseline is what this deployment does when nothing is in the way. Both
// figures are the best rate actually observed, never a constant: a number
// invented here would make every request on a slow GPU look like an incident.
type Baseline struct {
	// BestPrefillTokensPerSec is the fastest uncached-prompt-token rate seen within a TTFT.
	BestPrefillTokensPerSec float64 `json:"bestPrefillTokensPerSec"`
	// BestDecodeTokensPerSec is the fastest observed output-token rate.
	BestDecodeTokensPerSec float64 `json:"bestDecodeTokensPerSec"`
	// FloorTTFTMs is the shortest TTFT on a fully cached prompt: the round-trip cost.
	FloorTTFTMs int64 `json:"floorTtftMs"`
	// Samples is how many completed requests the baseline was drawn from.
	Samples int `json:"samples"`
	// MedianTotalMs is the rolling median wall time of recent requests.
	MedianTotalMs int64 `json:"medianTotalMs"`
}

// Ready reports whether enough requests have completed for a comparison to mean anything.
func (b Baseline) Ready() bool { return b.Samples >= minBaselineSamples }

const minBaselineSamples = 5

// Diagnoser learns the deployment's baseline and explains each request
// against it.
type Diagnoser struct {
	mu         sync.RWMutex
	base       Baseline
	totals     []int64
	totalsCap  int
	slowFloorM int64
	slowFactor float64
}

// New returns a diagnoser. `slowFloorMs` is the wall time below which a
// request is never called slow however far it sits above the median, and
// `slowFactor` is the multiple of the rolling median that does call it slow.
func New(slowFloorMs int64, slowFactor float64) *Diagnoser {
	if slowFloorMs <= 0 {
		slowFloorMs = 2000
	}
	if slowFactor <= 1 {
		slowFactor = 2.5
	}
	return &Diagnoser{totalsCap: 200, slowFloorM: slowFloorMs, slowFactor: slowFactor}
}

// Baseline returns the learned baseline.
func (d *Diagnoser) Baseline() Baseline {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.base
}

// Observe folds a completed request into the baseline.
func (d *Diagnoser) Observe(r requests.Record) {
	if r.Status != requests.StatusDone {
		return
	}
	total := r.TotalMs()
	if total < 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	d.totals = append(d.totals, total)
	if len(d.totals) > d.totalsCap {
		d.totals = d.totals[len(d.totals)-d.totalsCap:]
	}
	d.base.Samples++
	sorted := append([]int64(nil), d.totals...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	d.base.MedianTotalMs = sorted[len(sorted)/2]

	// Only a streamed response puts a TTFT on the wire, so only it sets these baselines.
	if !r.Stream {
		return
	}
	uncached := uncachedTokens(r)
	if ttft := r.TTFTMs(); ttft > 0 {
		if uncached > 0 {
			rate := float64(uncached) / (float64(ttft) / 1000)
			if rate > d.base.BestPrefillTokensPerSec {
				d.base.BestPrefillTokensPerSec = rate
			}
		} else if d.base.FloorTTFTMs == 0 || ttft < d.base.FloorTTFTMs {
			d.base.FloorTTFTMs = ttft
		}
	}
	if rate := r.OutputTokensPerSec(); rate > d.base.BestDecodeTokensPerSec {
		d.base.BestDecodeTokensPerSec = rate
	}
}

func uncachedTokens(r requests.Record) int {
	if r.PromptTokens <= 0 {
		return 0
	}
	cached := r.CachedTokens
	if cached < 0 {
		cached = 0
	}
	if cached > r.PromptTokens {
		cached = r.PromptTokens
	}
	return r.PromptTokens - cached
}

// Explain attributes a single finished request's wall time.
func (d *Diagnoser) Explain(r requests.Record) requests.Diagnosis {
	d.mu.RLock()
	base := d.base
	floor, factor := d.slowFloorM, d.slowFactor
	d.mu.RUnlock()

	total := r.TotalMs()
	// Causes is allocated up front: a JSON null here would break the panel.
	dg := requests.Diagnosis{ThresholdMs: floor, Causes: []requests.Cause{}}
	if total <= 0 {
		dg.Notes = append(dg.Notes, "the request has no finish time, so nothing can be attributed")
		return dg
	}
	threshold := floor
	if base.Ready() {
		if scaled := int64(float64(base.MedianTotalMs) * factor); scaled > threshold {
			threshold = scaled
		}
		dg.ThresholdMs = threshold
	}
	dg.Slow = total >= threshold

	add := func(c requests.Cause) {
		if c.Ms <= 0 {
			return
		}
		c.Share = float64(c.Ms) / float64(total)
		dg.Causes = append(dg.Causes, c)
	}

	if proxy := r.SentAt - r.ArrivedAt; proxy > 0 {
		add(requests.Cause{
			Factor: "proxy_overhead", Ms: proxy, Confidence: cache.Measured,
			Detail: "the dashboard held the request this long before it reached the server",
		})
	}

	// Without a stream there is no TTFT, so the whole server-side span is
	// opaque. A split here invents a prefill boundary the wire never showed.
	if !r.Stream {
		if gen := total - (r.SentAt - r.ArrivedAt); gen > 0 {
			add(requests.Cause{
				Factor: "generation", Ms: gen, Confidence: cache.Measured,
				Detail: "the whole server-side span: queueing, prefill and decode together",
			})
		}
		dg.Notes = append(dg.Notes, "the response was not streamed, so the server's first-token time never reached the wire and prefill cannot be separated from decode — send stream:true to get the split")
		finish(&dg, total)
		return dg
	}

	ttft := r.TTFTMs()
	uncached := uncachedTokens(r)
	if ttft > 0 {
		prefillMs := ttft - (r.SentAt - r.ArrivedAt)
		if prefillMs < 0 {
			prefillMs = 0
		}
		// The round-trip floor is the part of TTFT that is neither prefill nor queueing.
		fixed := base.FloorTTFTMs
		if fixed > prefillMs {
			fixed = prefillMs
		}
		work := prefillMs - fixed

		// Compute is what the best observed prefill rate takes. The rest is waiting.
		computeMs := int64(0)
		if base.BestPrefillTokensPerSec > 0 && uncached > 0 {
			computeMs = int64(float64(uncached) / base.BestPrefillTokensPerSec * 1000)
			if computeMs > work {
				computeMs = work
			}
		}
		queueMs := work - computeMs

		if computeMs > 0 {
			conf := cache.Inferred
			detail := fmt.Sprintf("%d of %d prompt tokens were not cached; at the fastest prefill rate seen here (%.0f tok/s) they cost about this much",
				uncached, r.PromptTokens, base.BestPrefillTokensPerSec)
			if !base.Ready() {
				detail += " — the baseline has only " + fmt.Sprint(base.Samples) + " samples so far"
			}
			add(requests.Cause{Factor: "prefill_uncached_tokens", Ms: computeMs, Confidence: conf, Detail: detail})
		}
		if queueMs > 0 {
			detail := "time before the first token that neither the round-trip floor nor prefill accounts for"
			if r.QueueDepthAtArrival > 0 {
				detail += fmt.Sprintf("; %.0f requests were already queued and %.0f running when it arrived", r.QueueDepthAtArrival, r.RunningAtArrival)
			} else if r.QueueDepthAtArrival == 0 {
				detail += "; the upstream queue was empty on arrival, so the wait was inside the server, not in front of it"
			}
			add(requests.Cause{Factor: "queue_wait", Ms: queueMs, Confidence: cache.Inferred, Detail: detail})
		}
		if fixed > 0 {
			add(requests.Cause{
				Factor: "round_trip_floor", Ms: fixed, Confidence: cache.Inferred,
				Detail: fmt.Sprintf("the shortest first-token time seen with a fully cached prompt is %dms", base.FloorTTFTMs),
			})
		}
	}

	if decode := r.DecodeMs(); decode > 0 {
		rate := r.OutputTokensPerSec()
		idealMs := int64(0)
		if base.BestDecodeTokensPerSec > 0 && r.CompletionTokens > 0 {
			idealMs = int64(float64(r.CompletionTokens) / base.BestDecodeTokensPerSec * 1000)
			if idealMs > decode {
				idealMs = decode
			}
		}
		add(requests.Cause{
			Factor: "decode", Ms: idealMs, Confidence: cache.Inferred,
			Detail: fmt.Sprintf("%d output tokens at the fastest decode rate seen here (%.0f tok/s)", r.CompletionTokens, base.BestDecodeTokensPerSec),
		})
		if slowdown := decode - idealMs; slowdown > 0 && idealMs > 0 {
			add(requests.Cause{
				Factor: "decode_contention", Ms: slowdown, Confidence: cache.Inferred,
				Detail: fmt.Sprintf("it decoded at %.1f tok/s against a best of %.1f — the gap is what sharing the batch cost", rate, base.BestDecodeTokensPerSec),
			})
		}
	}

	if r.CacheMatch.Shortfall > 0 {
		dg.Notes = append(dg.Notes, fmt.Sprintf(
			"the reconstructed prefix tree expected %d cached tokens and the server reported %d; that shortfall is why the prefill was longer than the prompt's novelty suggests",
			r.CacheMatch.ExpectedCachedTokens, r.CacheMatch.ReportedCachedTokens))
	}
	if r.TokenUsageAtArrival >= 0.9 {
		dg.Notes = append(dg.Notes, fmt.Sprintf("KV token usage was %.0f%% on arrival, so the server had little room to admit it", r.TokenUsageAtArrival*100))
	}
	if !base.Ready() {
		dg.Notes = append(dg.Notes, fmt.Sprintf("the baseline is drawn from %d completed requests; treat the split as provisional", base.Samples))
	}

	finish(&dg, total)
	return dg
}

// finish orders the causes and books the remainder. Attribution never silently
// swallows what it could not place: whatever the factors do not cover is its
// own slice, so the shares always add up to the wall time.
func finish(dg *requests.Diagnosis, total int64) {
	sort.SliceStable(dg.Causes, func(i, j int) bool { return dg.Causes[i].Ms > dg.Causes[j].Ms })
	if len(dg.Causes) > 0 {
		dg.Headline = dg.Causes[0].Factor
	}
	var covered int64
	for _, c := range dg.Causes {
		covered += c.Ms
	}
	if gap := total - covered; gap > 0 {
		dg.Causes = append(dg.Causes, requests.Cause{
			Factor: "unattributed", Ms: gap, Share: float64(gap) / float64(total),
			Confidence: cache.Derived,
			Detail:     "wall time none of the factors above accounts for",
		})
	}
}
