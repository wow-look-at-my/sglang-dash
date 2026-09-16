package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wow-look-at-my/sglang-dash/internal/cache"
	"github.com/wow-look-at-my/sglang-dash/internal/diagnose"
	"github.com/wow-look-at-my/sglang-dash/internal/events"
	"github.com/wow-look-at-my/sglang-dash/internal/metrics"
	"github.com/wow-look-at-my/sglang-dash/internal/requests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var sharedPrompt = strings.Repeat("system: be brief and never invent an order number. ", 20)

func newHub(t *testing.T) *Hub {
	t.Helper()
	return NewHub(
		events.NewBus(200), metrics.NewStore(100), cache.New(1000, false),
		requests.NewStore(100), diagnose.New(2000, 2.5), "proxy", "http://upstream:30000",
	)
}

func scrape(t *testing.T, h *Hub, body string) {
	t.Helper()
	samples, err := metrics.Parse(strings.NewReader(body))
	require.NoError(t, err)
	h.Metrics.Ingest(time.Now().UnixMilli(), samples)
}

// run drives a single whole request through the hub the way the proxy does.
func run(t *testing.T, h *Hub, id, prompt string, promptTokens, cachedTokens int, ttft, total int64) requests.Record {
	t.Helper()
	start := time.Now().Add(-time.Duration(total) * time.Millisecond)
	rec := requests.Record{
		ID: id, Path: "/v1/chat/completions", Model: "m/1", Stream: true,
		Status: requests.StatusInFlight, ArrivedAt: start.UnixMilli(),
		SentAt: start.UnixMilli() + 1, PromptChars: len([]rune(prompt)), CachedTokens: -1,
	}
	rec = h.Started(rec, prompt)
	rec.FirstTokenAt = rec.ArrivedAt + ttft
	h.FirstToken(id, time.UnixMilli(rec.FirstTokenAt))

	rec, _ = h.Requests.Get(id)
	rec.Status = requests.StatusDone
	rec.HTTPCode = http.StatusOK
	rec.FinishedAt = rec.ArrivedAt + total
	rec.PromptTokens = promptTokens
	rec.CachedTokens = cachedTokens
	rec.CompletionTokens = 100
	h.Finished(rec)

	got, ok := h.Requests.Get(id)
	require.True(t, ok)
	return got
}

func eventKinds(h *Hub) map[events.Kind]int {
	out := map[events.Kind]int{}
	for _, e := range h.Events.Recent(0) {
		out[e.Kind]++
	}
	return out
}

func TestStartedRecordsTheServerStateOnArrival(t *testing.T) {
	h := newHub(t)
	scrape(t, h, "sglang:num_queue_reqs 4\nsglang:num_running_reqs 2\nsglang:token_usage 0.6\n")

	rec := h.Started(requests.Record{ID: "a", ArrivedAt: time.Now().UnixMilli(), CachedTokens: -1}, sharedPrompt)
	assert.Equal(t, 4.0, rec.QueueDepthAtArrival)
	assert.Equal(t, 2.0, rec.RunningAtArrival)
	assert.InDelta(t, 0.6, rec.TokenUsageAtArrival, 1e-9)
	assert.Equal(t, 1, eventKinds(h)[events.KindRequestArrived])
}

func TestStartedMarksTheGaugesAbsentWithoutMetrics(t *testing.T) {
	h := newHub(t)
	rec := h.Started(requests.Record{ID: "a", ArrivedAt: time.Now().UnixMilli(), CachedTokens: -1}, sharedPrompt)
	assert.Equal(t, -1.0, rec.QueueDepthAtArrival)
	assert.Equal(t, -1.0, rec.RunningAtArrival)
	assert.Equal(t, -1.0, rec.TokenUsageAtArrival)
}

// The prompt is folded into the tree a single time, at completion. Folding
// at arrival as well would count every request again.
func TestAPromptIsCountedOnceAcrossItsWholeLifecycle(t *testing.T) {
	h := newHub(t)
	run(t, h, "a", sharedPrompt+"one", 300, -1, 50, 500)
	assert.Equal(t, 1, h.Cache.Snapshot(0).Stats.TotalRequests)
}

func TestASecondRequestOnTheSamePrefixIsReportedAsAHit(t *testing.T) {
	h := newHub(t)
	run(t, h, "a", sharedPrompt+"one", 300, 0, 50, 500)
	run(t, h, "b", sharedPrompt+"two", 300, 220, 30, 400)

	counts := eventKinds(h)
	assert.Equal(t, 1, counts[events.KindCacheMiss])
	assert.Equal(t, 1, counts[events.KindCacheHit])
	assert.Equal(t, 2, counts[events.KindRequestCompleted])
}

func TestAResponseWithoutACachedCountSaysTheHitIsUnknown(t *testing.T) {
	h := newHub(t)
	run(t, h, "a", sharedPrompt+"one", 300, -1, 50, 500)
	assert.Equal(t, 1, eventKinds(h)[events.KindCacheInserted])
	assert.Zero(t, eventKinds(h)[events.KindCacheMiss])
}

func TestALostPrefixIsPublishedWithItsEvidence(t *testing.T) {
	h := newHub(t)
	h.Cache.SetSignals(func() cache.Signals {
		return cache.Signals{MetricsOK: true, TokenUsage: 0.96}
	})
	run(t, h, "a", sharedPrompt+"one", 300, -1, 50, 500)
	run(t, h, "b", sharedPrompt+"one", 300, 0, 50, 500)

	require.Equal(t, 1, eventKinds(h)[events.KindCacheEvicted])
	for _, e := range h.Events.Recent(0) {
		if e.Kind != events.KindCacheEvicted {
			continue
		}
		assert.Equal(t, "capacity_pressure", e.Fields["reason"])
		assert.NotEmpty(t, e.Fields["evidence"])
		return
	}
}

func TestAFinishedRequestCarriesItsDiagnosis(t *testing.T) {
	h := newHub(t)
	got := run(t, h, "a", sharedPrompt+"one", 300, 0, 50, 500)
	require.NotNil(t, got.Diagnosis)
	assert.NotEmpty(t, got.Diagnosis.Causes)
	assert.NotEmpty(t, got.Diagnosis.Headline)
}

func TestASlowRequestPublishesWhyItWasSlow(t *testing.T) {
	h := newHub(t)
	for i := range 8 {
		run(t, h, "fast"+string(rune('a'+i)), sharedPrompt+"one", 300, 280, 20, 200)
	}
	got := run(t, h, "slow", sharedPrompt+"two", 300, 0, 4000, 9000)

	require.NotNil(t, got.Diagnosis)
	assert.True(t, got.Diagnosis.Slow)
	assert.Equal(t, 1, eventKinds(h)[events.KindSlowDetected])
	assert.Equal(t, 1, h.Requests.Totals().Slow)
}

func TestAFailedRequestIsPublishedAsAFailure(t *testing.T) {
	h := newHub(t)
	h.Finished(requests.Record{
		ID: "bad", Status: requests.StatusFailed, Error: "upstream returned 503",
		ArrivedAt: time.Now().UnixMilli(), FinishedAt: time.Now().UnixMilli() + 5, CachedTokens: -1,
	})
	assert.Equal(t, 1, eventKinds(h)[events.KindRequestFailed])
	assert.Equal(t, 1, h.Requests.Totals().Failed)
}

func TestAnAbortedRequestIsPublishedAsAnAbort(t *testing.T) {
	h := newHub(t)
	h.Finished(requests.Record{
		ID: "gone", Status: requests.StatusAborted, Error: "the client went away",
		ArrivedAt: time.Now().UnixMilli(), FinishedAt: time.Now().UnixMilli() + 5, CachedTokens: -1,
	})
	assert.Equal(t, 1, eventKinds(h)[events.KindRequestAborted])
}

func TestFirstTokenOnAnUnknownRequestIsIgnored(t *testing.T) {
	h := newHub(t)
	assert.NotPanics(t, func() { h.FirstToken("never-started", time.Now()) })
	assert.Zero(t, eventKinds(h)[events.KindRequestFirstTok])
}

// --- the HTTP surface -------------------------------------------------------

func serve(t *testing.T, h *Hub) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func getJSON(t *testing.T, url string, into any) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	if into != nil && resp.StatusCode == http.StatusOK {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(into))
	}
	return resp
}

func TestStatusReportsTheModeAndTheTarget(t *testing.T) {
	h := newHub(t)
	scrape(t, h, "sglang:token_usage 0.3\n")
	server := serve(t, h)

	var status Status
	getJSON(t, server.URL+"/api/status", &status)
	assert.Equal(t, "proxy", status.Mode)
	assert.Equal(t, "http://upstream:30000", status.Upstream)
	assert.True(t, status.Metrics.Ok)
	assert.Contains(t, status.KnownSeries, "sglang:token_usage")
}

func TestRequestsAndEventsAreServedAsArrays(t *testing.T) {
	h := newHub(t)
	run(t, h, "a", sharedPrompt+"one", 300, 0, 50, 500)
	server := serve(t, h)

	var reqs struct {
		Requests []requests.Record `json:"requests"`
	}
	getJSON(t, server.URL+"/api/requests", &reqs)
	require.Len(t, reqs.Requests, 1)
	assert.Equal(t, "a", reqs.Requests[0].ID)

	var evs struct {
		Events []events.Event `json:"events"`
	}
	getJSON(t, server.URL+"/api/events?limit=5", &evs)
	assert.NotEmpty(t, evs.Events)
}

func TestOneRequestIsServedByID(t *testing.T) {
	h := newHub(t)
	run(t, h, "a", sharedPrompt+"one", 300, 0, 50, 500)
	server := serve(t, h)

	var rec requests.Record
	getJSON(t, server.URL+"/api/requests/a", &rec)
	assert.Equal(t, "a", rec.ID)

	resp := getJSON(t, server.URL+"/api/requests/missing", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestTheCacheSnapshotIsServedWithItsSlicesAsArrays(t *testing.T) {
	server := serve(t, newHub(t))

	var raw map[string]json.RawMessage
	getJSON(t, server.URL+"/api/cache", &raw)
	for _, key := range []string{"nodes", "roots", "lru", "evictions"} {
		assert.NotEqual(t, "null", string(raw[key]), "%s must be an array even when empty", key)
	}
}

func TestASeriesHistoryIsServedAndAnUnknownOneIsRefused(t *testing.T) {
	h := newHub(t)
	scrape(t, h, "sglang:token_usage 0.3\n")
	server := serve(t, h)

	var series struct {
		Key    string          `json:"key"`
		Points []metrics.Point `json:"points"`
	}
	getJSON(t, server.URL+"/api/metrics/series?key=sglang:token_usage", &series)
	assert.Len(t, series.Points, 1)

	assert.Equal(t, http.StatusBadRequest, getJSON(t, server.URL+"/api/metrics/series", nil).StatusCode)
	assert.Equal(t, http.StatusNotFound, getJSON(t, server.URL+"/api/metrics/series?key=nope", nil).StatusCode)
}

func TestAnUnreadableLimitFallsBackToTheDefault(t *testing.T) {
	h := newHub(t)
	run(t, h, "a", sharedPrompt+"one", 300, 0, 50, 500)
	server := serve(t, h)

	var reqs struct {
		Requests []requests.Record `json:"requests"`
	}
	getJSON(t, server.URL+"/api/requests?limit=banana", &reqs)
	assert.Len(t, reqs.Requests, 1)
}

func TestTheStreamOpensWithAStatusFrameThenCarriesEvents(t *testing.T) {
	h := newHub(t)
	server := serve(t, h)

	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/stream", nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()

	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	reader := bufio.NewReader(resp.Body)
	first := readFrame(t, reader)
	assert.Equal(t, "status", first.Type)
	require.NotNil(t, first.Status)
	assert.Equal(t, "proxy", first.Status.Mode)

	h.Events.Publish(events.KindDashboardNotice, "a thing happened", nil)
	second := readFrame(t, reader)
	require.NotNil(t, second.Event)
	assert.Equal(t, "a thing happened", second.Event.Message)
}

func readFrame(t *testing.T, reader *bufio.Reader) streamPayload {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var payload streamPayload
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload))
		return payload
	}
}
