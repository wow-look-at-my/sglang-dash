package demo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The simulator exists to exercise the dashboard's own detectors. A pool this
// traffic never fills would leave the eviction path dark, so the sizing is a
// property worth asserting rather than a number to eyeball on screen.
func TestTheSimulationReachesCachePressureAndLosesAPrefix(t *testing.T) {
	r, hub := newRunner(t)
	r.SetPace(400)
	for range 400 {
		r.oneRequest(context.Background())
		r.publishMetrics()
	}

	stats := hub.Cache.Snapshot(0).Stats
	require.Positive(t, stats.Evictions, "the demo never produced an eviction to explain")
	assert.Positive(t, stats.EvictedTokens)

	snapshot := hub.Cache.Snapshot(50)
	require.NotEmpty(t, snapshot.Evictions)
	reasons := map[string]int{}
	for _, ev := range snapshot.Evictions {
		assert.NotEmpty(t, ev.Reason)
		assert.NotEmpty(t, ev.Evidence, "an eviction without evidence cannot be argued with")
		reasons[ev.Reason]++
	}
	// The simulated server only drops a prefix when its pool is full, so the
	// classifier must be able to say so. A run that produced nothing but
	// "unknown" means the signals never reached it.
	assert.Positive(t, reasons["capacity_pressure"], "every eviction was classified %v", reasons)
}
