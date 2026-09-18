package metrics

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ingest(t *testing.T, s *Store, now int64, body string) Snapshot {
	t.Helper()
	samples, err := Parse(strings.NewReader(body))
	require.NoError(t, err)
	return s.Ingest(now, samples)
}

func TestIngestSumsEveryLabelSetOfAGauge(t *testing.T) {
	s := NewStore(10)
	snap := ingest(t, s, 1000, "sglang:num_running_reqs{a=\"1\"} 3\nsglang:num_running_reqs{a=\"2\"} 4\n")
	assert.True(t, snap.Ok)
	assert.Equal(t, 7.0, snap.Gauges["running_reqs"])
}

func TestIngestReportsAbsentGauges(t *testing.T) {
	s := NewStore(10)
	snap := ingest(t, s, 1000, "sglang:token_usage 0.5\n")
	assert.Contains(t, snap.Missing, "running_reqs")
	assert.NotContains(t, snap.Missing, "token_usage")
	_, present := snap.Gauges["running_reqs"]
	assert.False(t, present)
}

func TestIngestFallsBackToAnAlternateSpelling(t *testing.T) {
	s := NewStore(10)
	snap := ingest(t, s, 1000, "sglang:num_waiting_requests 5\n")
	assert.Equal(t, 5.0, snap.Gauges["queued_reqs"])
	assert.NotContains(t, snap.Missing, "queued_reqs")
}

// The quantile is interpolated inside the bucket that crosses the target.
func TestQuantilesInterpolateInsideTheWinningBucket(t *testing.T) {
	s := NewStore(10)
	snap := ingest(t, s, 1000, strings.Join([]string{
		`sglang:e2e_request_latency_seconds_bucket{le="1"} 50`,
		`sglang:e2e_request_latency_seconds_bucket{le="2"} 100`,
		`sglang:e2e_request_latency_seconds_bucket{le="+Inf"} 100`,
		`sglang:e2e_request_latency_seconds_sum 120`,
		`sglang:e2e_request_latency_seconds_count 100`,
		"",
	}, "\n"))

	q := snap.Quantiles["e2e_latency"]
	assert.Equal(t, 100.0, q.Count)
	assert.Equal(t, 120.0, q.Sum)
	assert.InDelta(t, 1.0, q.P50, 1e-9)
	assert.InDelta(t, 1.8, q.P90, 1e-9)
	assert.NotEqual(t, 2.0, q.P99, "a bucket edge means the value was not interpolated")
}

func TestQuantilesFallBackOffTheInfinityBucket(t *testing.T) {
	s := NewStore(10)
	snap := ingest(t, s, 1000, strings.Join([]string{
		`sglang:time_to_first_token_seconds_bucket{le="0.5"} 90`,
		`sglang:time_to_first_token_seconds_bucket{le="+Inf"} 100`,
		"",
	}, "\n"))
	// The unbounded bucket has no upper edge, so the last finite edge is honest.
	assert.InDelta(t, 0.5, snap.Quantiles["ttft"].P99, 1e-9)
}

func TestQuantilesAreAbsentWithoutAHistogram(t *testing.T) {
	s := NewStore(10)
	snap := ingest(t, s, 1000, "sglang:token_usage 0.5\n")
	_, present := snap.Quantiles["ttft"]
	assert.False(t, present)
}

func TestFailClearsTheGauges(t *testing.T) {
	s := NewStore(10)
	ingest(t, s, 1000, "sglang:token_usage 0.5\n")
	snap := s.Fail(2000, errors.New("connection refused"))
	assert.False(t, snap.Ok)
	assert.Empty(t, snap.Gauges)
	assert.Equal(t, "connection refused", snap.Error)
	assert.Equal(t, snap, s.Last())
}

func TestHistoryKeepsPointsInScrapeOrder(t *testing.T) {
	s := NewStore(3)
	for i := range 5 {
		ingest(t, s, int64(1000+i), "sglang:token_usage 0.5\n")
	}
	points, ok := s.History("sglang:token_usage")
	require.True(t, ok)
	require.Len(t, points, 3, "the ring keeps only its capacity")
	assert.Equal(t, int64(1002), points[0].Time)
	assert.Equal(t, int64(1004), points[2].Time)

	_, ok = s.History("sglang:never_scraped")
	assert.False(t, ok, "a series never seen is not the same as one reading zero")
}

func TestNamesListsEverySeries(t *testing.T) {
	s := NewStore(4)
	ingest(t, s, 1000, "b 1\na 2\n")
	assert.Equal(t, []string{"a", "b"}, s.Names())
}

func TestNewStoreClampsATinyCapacity(t *testing.T) {
	s := NewStore(0)
	ingest(t, s, 1000, "a 1\n")
	ingest(t, s, 1001, "a 2\n")
	points, ok := s.History("a")
	require.True(t, ok)
	assert.Len(t, points, 2)
}
