package demo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wow-look-at-my/go-containers/set"
	"github.com/wow-look-at-my/sglang-dash/internal/api"
	"github.com/wow-look-at-my/sglang-dash/internal/cache"
	"github.com/wow-look-at-my/sglang-dash/internal/diagnose"
	"github.com/wow-look-at-my/sglang-dash/internal/events"
	"github.com/wow-look-at-my/sglang-dash/internal/metrics"
	"github.com/wow-look-at-my/sglang-dash/internal/requests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRunner wires the collectors the way main.go does in demo mode, signals
// included. Without them every eviction would be classified "unknown".
func newRunner(t *testing.T) (*Runner, *api.Hub) {
	t.Helper()
	store := metrics.NewStore(100)
	tree := cache.New(2000, false)
	tree.SetSignals(func() cache.Signals { return api.SignalsFrom(store) })
	hub := api.NewHub(
		events.NewBus(500), store, tree,
		requests.NewStore(500), diagnose.New(2000, 2.5), "demo", "",
	)
	return New(hub, 1), hub
}

// The simulator writes real exposition text through the real parser, so the
// demo exercises the same code path a live server does.
func TestThePublishedMetricsParse(t *testing.T) {
	r, hub := newRunner(t)
	r.publishMetrics()

	snap := hub.Metrics.Last()
	require.True(t, snap.Ok, "the simulator produced metrics the parser rejected")
	assert.Contains(t, snap.Gauges, "token_usage")
	assert.Contains(t, snap.Gauges, "running_reqs")
	assert.Contains(t, snap.Quantiles, "ttft")
	assert.Contains(t, snap.Quantiles, "e2e_latency")
}

func TestOneSimulatedRequestReachesEveryCollector(t *testing.T) {
	r, hub := newRunner(t)
	r.oneRequest(context.Background())

	recent := hub.Requests.Recent(0)
	require.Len(t, recent, 1)
	rec := recent[0]
	assert.Equal(t, requests.StatusDone, rec.Status)
	assert.Positive(t, rec.PromptTokens)
	assert.Positive(t, rec.CompletionTokens)
	assert.Positive(t, rec.TotalMs())
	require.NotNil(t, rec.Diagnosis)
	assert.NotEmpty(t, rec.Diagnosis.Causes)

	assert.Positive(t, hub.Cache.Snapshot(0).Stats.Nodes)
	assert.NotEmpty(t, hub.Events.Recent(0))
}

// Many requests share a system prompt, so the prefix cache has something to
// hit. A simulator where every prompt is novel would exercise nothing.
func TestRepeatedTrafficProducesCacheHits(t *testing.T) {
	r, hub := newRunner(t)
	r.SetPace(200)
	for range 30 {
		r.oneRequest(context.Background())
	}
	stats := hub.Cache.Snapshot(0).Stats
	assert.Positive(t, stats.TotalHits)
	assert.Positive(t, stats.HitTokens)
	assert.Greater(t, stats.TotalRequests, 1)
}

func TestACancelledContextStopsTheSimulation(t *testing.T) {
	r, _ := newRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return when its context ended")
	}
}

func TestRunDrivesTrafficUntilItsContextEnds(t *testing.T) {
	r, hub := newRunner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	r.Run(ctx)

	assert.Positive(t, hub.Requests.Totals().Seen)
	assert.True(t, hub.Metrics.Last().Ok)
}

func TestTheHistogramHelperEmitsAParseableFamily(t *testing.T) {
	text := histogram("m_seconds", []bucketRow{{"0.5", 4}, {"+Inf", 10}})
	samples, err := metrics.Parse(strings.NewReader(text))
	require.NoError(t, err)

	names := set.New[string]()
	for _, s := range samples {
		names.Add(s.Name)
	}
	assert.True(t, names.Contains("m_seconds_bucket"))
	assert.True(t, names.Contains("m_seconds_sum"))
	assert.True(t, names.Contains("m_seconds_count"))
}

func TestRandomWordsProducesTheRequestedLength(t *testing.T) {
	r, _ := newRunner(t)
	assert.Len(t, strings.Fields(randomWords(r.rng, 12)), 12)
}

func TestHeadTrimsToTheLimit(t *testing.T) {
	assert.Equal(t, "abc", head("abc", 10))
	assert.Equal(t, "ab", head("abcdef", 2))
}
