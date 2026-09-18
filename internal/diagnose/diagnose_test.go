package diagnose

import (
	"testing"

	"github.com/wow-look-at-my/sglang-dash/internal/cache"
	"github.com/wow-look-at-my/sglang-dash/internal/requests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func streamed(id string, arrived, sent, firstToken, finished int64, promptTokens, cachedTokens, outTokens int) requests.Record {
	return requests.Record{
		ID: id, Stream: true, Status: requests.StatusDone,
		ArrivedAt: arrived, SentAt: sent, FirstTokenAt: firstToken, FinishedAt: finished,
		PromptTokens: promptTokens, CachedTokens: cachedTokens, CompletionTokens: outTokens,
		QueueDepthAtArrival: -1, RunningAtArrival: -1, TokenUsageAtArrival: -1,
	}
}

// Feeds the diagnoser enough fast requests to make its baseline usable.
func warmBaseline(d *Diagnoser) {
	for i := range minBaselineSamples + 1 {
		r := streamed("warm", 0, 1, 100, 1100, 1000, 0, 1000)
		r.ID = "warm" + string(rune('a'+i))
		d.Observe(r)
	}
}

func TestBaselineLearnsTheBestObservedRates(t *testing.T) {
	d := New(2000, 2.5)
	assert.False(t, d.Baseline().Ready())

	warmBaseline(d)
	base := d.Baseline()
	assert.True(t, base.Ready())
	assert.InDelta(t, 10000.0, base.BestPrefillTokensPerSec, 1e-6)
	assert.InDelta(t, 1000.0, base.BestDecodeTokensPerSec, 1e-6)
	assert.Equal(t, int64(1100), base.MedianTotalMs)
}

func TestBaselineLearnsTheRoundTripFloorFromAFullyCachedPrompt(t *testing.T) {
	d := New(2000, 2.5)
	d.Observe(streamed("cached", 0, 1, 25, 500, 800, 800, 100))
	assert.Equal(t, int64(25), d.Baseline().FloorTTFTMs)
}

// Only a streamed response puts a TTFT on the wire, so only a streamed
// response may set the prefill and decode baselines.
func TestANonStreamedResponseDoesNotTeachTheRateBaselines(t *testing.T) {
	d := New(2000, 2.5)
	r := streamed("whole", 0, 1, 900, 1000, 500, 0, 200)
	r.Stream = false
	d.Observe(r)

	base := d.Baseline()
	assert.Zero(t, base.BestPrefillTokensPerSec)
	assert.Zero(t, base.BestDecodeTokensPerSec)
	assert.Equal(t, 1, base.Samples, "it still counts toward the median")
	assert.Equal(t, int64(1000), base.MedianTotalMs)
}

func TestObserveIgnoresUnfinishedAndFailedRequests(t *testing.T) {
	d := New(2000, 2.5)
	d.Observe(requests.Record{Status: requests.StatusInFlight, ArrivedAt: 1})
	d.Observe(requests.Record{Status: requests.StatusFailed, ArrivedAt: 1, FinishedAt: 2})
	d.Observe(requests.Record{Status: requests.StatusDone, ArrivedAt: 1})
	assert.Zero(t, d.Baseline().Samples)
}

// Attribution never silently swallows what it could not place: the slices
// always add up to the wall time.
func TestTheSlicesAlwaysSumToTheWallTime(t *testing.T) {
	d := New(2000, 2.5)
	warmBaseline(d)

	r := streamed("slow", 0, 5, 900, 4000, 1000, 200, 500)
	r.QueueDepthAtArrival = 4
	r.RunningAtArrival = 2
	dg := d.Explain(r)

	var covered int64
	for _, c := range dg.Causes {
		covered += c.Ms
		assert.InDelta(t, float64(c.Ms)/float64(r.TotalMs()), c.Share, 1e-9)
	}
	assert.Equal(t, r.TotalMs(), covered)
}

func TestAQueueWaitIsNamedWithTheDepthOnArrival(t *testing.T) {
	d := New(2000, 2.5)
	warmBaseline(d)

	r := streamed("queued", 0, 1, 2000, 2500, 100, 100, 100)
	r.QueueDepthAtArrival = 7
	r.RunningAtArrival = 3

	causes := byFactor(d.Explain(r))
	require.Contains(t, causes, "queue_wait")
	assert.Contains(t, causes["queue_wait"].Detail, "7 requests were already queued")
	assert.Equal(t, cache.Inferred, causes["queue_wait"].Confidence)
}

func TestAnEmptyUpstreamQueueSaysTheWaitWasInsideTheServer(t *testing.T) {
	d := New(2000, 2.5)
	warmBaseline(d)

	r := streamed("queued", 0, 1, 2000, 2500, 100, 100, 100)
	r.QueueDepthAtArrival = 0
	causes := byFactor(d.Explain(r))
	require.Contains(t, causes, "queue_wait")
	assert.Contains(t, causes["queue_wait"].Detail, "inside the server")
}

func TestUncachedPromptTokensAreChargedToPrefill(t *testing.T) {
	d := New(2000, 2.5)
	warmBaseline(d)

	cached := d.Explain(streamed("hit", 0, 1, 300, 1300, 5000, 5000, 100))
	missed := d.Explain(streamed("miss", 0, 1, 300, 1300, 5000, 0, 100))

	assert.NotContains(t, byFactor(cached), "prefill_uncached_tokens",
		"a fully cached prompt has nothing to prefill")
	require.Contains(t, byFactor(missed), "prefill_uncached_tokens")
	assert.Contains(t, byFactor(missed)["prefill_uncached_tokens"].Detail, "5000 prompt tokens")
}

func TestDecodeContentionIsTheGapAgainstTheBestRate(t *testing.T) {
	d := New(2000, 2.5)
	warmBaseline(d)

	causes := byFactor(d.Explain(streamed("slow", 0, 1, 100, 4100, 10, 10, 1000)))
	require.Contains(t, causes, "decode")
	require.Contains(t, causes, "decode_contention")
	assert.Equal(t, int64(1000), causes["decode"].Ms)
	assert.Equal(t, int64(3000), causes["decode_contention"].Ms)
}

func TestANonStreamedResponseGetsOneOpaqueSpan(t *testing.T) {
	d := New(2000, 2.5)
	warmBaseline(d)

	r := streamed("whole", 0, 10, 3000, 3000, 500, 0, 200)
	r.Stream = false
	dg := d.Explain(r)

	causes := byFactor(dg)
	assert.Contains(t, causes, "generation")
	assert.NotContains(t, causes, "queue_wait")
	assert.NotContains(t, causes, "decode")
	assert.Equal(t, cache.Measured, causes["generation"].Confidence)
	assert.Contains(t, notes(dg), "not streamed")
}

func TestTheSlowThresholdTakesTheLargerOfFloorAndMedian(t *testing.T) {
	d := New(100, 3)
	for range minBaselineSamples + 1 {
		d.Observe(streamed("fast", 0, 1, 10, 1000, 10, 0, 10))
	}
	assert.Equal(t, int64(3000), d.Explain(streamed("x", 0, 1, 10, 2000, 10, 0, 10)).ThresholdMs)
	assert.False(t, d.Explain(streamed("x", 0, 1, 10, 2000, 10, 0, 10)).Slow)
	assert.True(t, d.Explain(streamed("x", 0, 1, 10, 9000, 10, 0, 10)).Slow)
}

func TestAColdBaselineUsesTheFloorAndSaysSo(t *testing.T) {
	d := New(2000, 2.5)
	dg := d.Explain(streamed("x", 0, 1, 10, 500, 10, 0, 10))
	assert.Equal(t, int64(2000), dg.ThresholdMs)
	assert.Contains(t, notes(dg), "provisional")
}

func TestNewClampsNonsenseSettings(t *testing.T) {
	d := New(0, 0)
	assert.Equal(t, int64(2000), d.Explain(streamed("x", 0, 1, 10, 500, 10, 0, 10)).ThresholdMs)
}

func TestAPrefixShortfallIsCalledOutInTheNotes(t *testing.T) {
	d := New(2000, 2.5)
	warmBaseline(d)

	r := streamed("short", 0, 1, 400, 1400, 900, 100, 100)
	r.CacheMatch = cache.Match{ExpectedCachedTokens: 700, ReportedCachedTokens: 100, Shortfall: 600}
	assert.Contains(t, notes(d.Explain(r)), "700 cached tokens")
}

func TestAFullPoolOnArrivalIsCalledOutInTheNotes(t *testing.T) {
	d := New(2000, 2.5)
	warmBaseline(d)

	r := streamed("tight", 0, 1, 400, 1400, 900, 100, 100)
	r.TokenUsageAtArrival = 0.97
	assert.Contains(t, notes(d.Explain(r)), "97%")
}

func TestARequestWithNoFinishTimeIsNotAttributed(t *testing.T) {
	dg := New(2000, 2.5).Explain(requests.Record{ArrivedAt: 1000})
	assert.Empty(t, dg.Causes)
	assert.Contains(t, notes(dg), "no finish time")
}

func TestProxyOverheadIsMeasuredNotApportioned(t *testing.T) {
	d := New(2000, 2.5)
	warmBaseline(d)
	causes := byFactor(d.Explain(streamed("x", 0, 40, 200, 1200, 100, 100, 100)))
	require.Contains(t, causes, "proxy_overhead")
	assert.Equal(t, int64(40), causes["proxy_overhead"].Ms)
	assert.Equal(t, cache.Measured, causes["proxy_overhead"].Confidence)
}

func byFactor(dg requests.Diagnosis) map[string]requests.Cause {
	out := map[string]requests.Cause{}
	for _, c := range dg.Causes {
		out[c.Factor] = c
	}
	return out
}

func notes(dg requests.Diagnosis) string {
	all := ""
	for _, n := range dg.Notes {
		all += n + "\n"
	}
	return all
}
