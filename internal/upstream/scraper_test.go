package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wow-look-at-my/sglang-dash/internal/events"
	"github.com/wow-look-at-my/sglang-dash/internal/metrics"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture serves whatever the test's current body is, so a scrape sequence can
// change what the server reports between ticks.
type fixture struct {
	body   atomic.Value
	status atomic.Int32
}

func (f *fixture) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	if code := f.status.Load(); code != 0 {
		http.Error(w, "boom", int(code))
		return
	}
	body, _ := f.body.Load().(string)
	w.Write([]byte(body))
}

func newScraper(t *testing.T, body string) (*Scraper, *fixture, *metrics.Store, *events.Bus) {
	t.Helper()
	f := &fixture{}
	f.body.Store(body)
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	u, err := url.Parse(server.URL + "/metrics")
	require.NoError(t, err)

	store := metrics.NewStore(100)
	bus := events.NewBus(100)
	return NewScraper(u, 10*time.Millisecond, store, bus), f, store, bus
}

func kinds(bus *events.Bus) map[events.Kind]int {
	out := map[events.Kind]int{}
	for _, e := range bus.Recent(0) {
		out[e.Kind]++
	}
	return out
}

func TestOneScrapeFillsTheStore(t *testing.T) {
	s, _, store, _ := newScraper(t, "sglang:token_usage 0.4\nsglang:num_running_reqs 2\n")
	s.scrapeOnce(context.Background())

	snap := store.Last()
	assert.True(t, snap.Ok)
	assert.InDelta(t, 0.4, snap.Gauges["token_usage"], 1e-9)
	assert.Equal(t, 2.0, snap.Gauges["running_reqs"])
}

func TestAFailedScrapeIsReportedOnceAndRecoveryToo(t *testing.T) {
	s, f, store, bus := newScraper(t, "sglang:token_usage 0.4\n")
	s.scrapeOnce(context.Background())

	f.status.Store(http.StatusInternalServerError)
	s.scrapeOnce(context.Background())
	s.scrapeOnce(context.Background())

	assert.False(t, store.Last().Ok)
	assert.Equal(t, 1, kinds(bus)[events.KindUpstreamDown], "a continuing outage is not news on every tick")

	f.status.Store(0)
	s.scrapeOnce(context.Background())
	assert.Equal(t, 1, kinds(bus)[events.KindUpstreamUp])
	assert.True(t, store.Last().Ok)
}

// A counter reading lower than the scrape before it means the server restarted,
// which empties its prefix cache.
func TestACounterGoingBackwardsReadsAsARestart(t *testing.T) {
	s, f, _, bus := newScraper(t, "sglang:num_requests_total 500\n")
	s.scrapeOnce(context.Background())
	assert.False(t, s.Signals().UpstreamRestarted)

	f.body.Store("sglang:num_requests_total 3\n")
	s.scrapeOnce(context.Background())

	assert.Equal(t, 1, kinds(bus)[events.KindCacheFlushed])
	assert.True(t, s.Signals().UpstreamRestarted)
}

func TestPressureIsEdgeTriggeredWithAGapBelowIt(t *testing.T) {
	s, f, _, bus := newScraper(t, "sglang:token_usage 0.5\n")
	s.scrapeOnce(context.Background())

	f.body.Store("sglang:token_usage 0.97\n")
	s.scrapeOnce(context.Background())
	s.scrapeOnce(context.Background())
	assert.Equal(t, 1, kinds(bus)[events.KindMemoryPressure], "a held threshold is reported once")

	// Inside the gap between the thresholds nothing changes either way.
	f.body.Store("sglang:token_usage 0.88\n")
	s.scrapeOnce(context.Background())
	assert.Zero(t, kinds(bus)[events.KindSchedRecovered])

	f.body.Store("sglang:token_usage 0.5\n")
	s.scrapeOnce(context.Background())
	assert.Equal(t, 1, kinds(bus)[events.KindSchedRecovered])
}

// A quiet tick between batches is normal, so a stall is only reported a
// single time it has outlasted several scrapes.
func TestAStallIsReportedOnlyAfterItPersists(t *testing.T) {
	s, _, _, bus := newScraper(t, "sglang:num_running_reqs 4\nsglang:gen_throughput 0\n")
	s.scrapeOnce(context.Background())
	assert.Zero(t, kinds(bus)[events.KindSchedStalled])

	time.Sleep(40 * time.Millisecond)
	s.scrapeOnce(context.Background())
	assert.Equal(t, 1, kinds(bus)[events.KindSchedStalled])

	s.scrapeOnce(context.Background())
	assert.Equal(t, 1, kinds(bus)[events.KindSchedStalled], "a continuing stall is not re-reported")
}

func TestThroughputResumingClearsTheStall(t *testing.T) {
	s, f, _, bus := newScraper(t, "sglang:num_running_reqs 4\nsglang:gen_throughput 0\n")
	s.scrapeOnce(context.Background())
	time.Sleep(40 * time.Millisecond)
	s.scrapeOnce(context.Background())
	require.Equal(t, 1, kinds(bus)[events.KindSchedStalled])

	f.body.Store("sglang:num_running_reqs 4\nsglang:gen_throughput 120\n")
	s.scrapeOnce(context.Background())
	assert.Equal(t, 1, kinds(bus)[events.KindSchedRecovered])
}

func TestUnparseableMetricsFailTheScrape(t *testing.T) {
	s, _, store, _ := newScraper(t, "this is not exposition\n")
	s.scrapeOnce(context.Background())
	assert.False(t, store.Last().Ok)
	assert.Contains(t, store.Last().Error, "line 1")
}

func TestSignalsReportWhetherMetricsAreUsable(t *testing.T) {
	s, f, _, _ := newScraper(t, "sglang:token_usage 0.7\nsglang:num_running_reqs 3\nsglang:num_used_tokens 900\n")
	s.scrapeOnce(context.Background())

	sig := s.Signals()
	assert.True(t, sig.MetricsOK)
	assert.InDelta(t, 0.7, sig.TokenUsage, 1e-9)
	assert.Equal(t, 3.0, sig.RunningRequests)
	assert.Equal(t, 900.0, sig.UsedTokens)

	f.status.Store(http.StatusBadGateway)
	s.scrapeOnce(context.Background())
	assert.False(t, s.Signals().MetricsOK)
}

func TestRunPollsUntilTheContextEnds(t *testing.T) {
	s, _, store, _ := newScraper(t, "sglang:token_usage 0.4\n")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return when its context ended")
	}
	// Cancellation can land mid-scrape, so the closing snapshot is allowed to
	// be a failure. What Run must leave behind is a recorded scrape.
	points, ok := store.History("sglang:token_usage")
	require.True(t, ok, "Run never completed a scrape")
	assert.NotEmpty(t, points)
}
