package cache

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedHead spans several chunks, so prompts built from it overlap on whole chunk boundaries.
var sharedHead = strings.Repeat("system: answer briefly and never invent an order number. ", 20)

func promptWith(tail string) string { return sharedHead + "user: " + tail }

func observe(t *testing.T, tree *Tree, id, prompt string, promptTokens, cachedTokens int) (Match, []Eviction) {
	t.Helper()
	return tree.Observe(Observation{
		RequestID: id, Time: time.Now(), Prompt: prompt,
		PromptTokens: promptTokens, CachedTokens: cachedTokens,
	})
}

func TestChunksSplitOnFixedWindows(t *testing.T) {
	assert.Nil(t, Chunks(""))
	chunks := Chunks(strings.Repeat("x", ChunkChars+5))
	require.Len(t, chunks, 2)
	assert.Len(t, chunks[0], ChunkChars)
	assert.Len(t, chunks[1], 5)
}

func TestObserveSharesTheChunksTwoPromptsHaveInCommon(t *testing.T) {
	tree := New(1000, false)
	first, _ := observe(t, tree, "r1", promptWith("where is my order"), 300, -1)
	second, _ := observe(t, tree, "r2", promptWith("cancel my order"), 300, -1)

	assert.Empty(t, first.ReusedNodes, "nothing existed for the opening prompt to reuse")
	assert.NotEmpty(t, second.ReusedNodes, "the shared system prompt must be reused")
	assert.NotEmpty(t, second.NewNodes, "the differing tail must add nodes")
	assert.Greater(t, second.ExpectedCachedTokens, 0)
}

// Walk answers what a prompt would match without touching the tree. Folding a
// prompt in at arrival and again at completion would count each request again.
func TestWalkChangesNothing(t *testing.T) {
	tree := New(1000, false)
	observe(t, tree, "r1", promptWith("a"), 300, -1)
	before := tree.Snapshot(10)

	walk := tree.Walk(promptWith("a"))
	after := tree.Snapshot(10)

	assert.NotEmpty(t, walk.ReusedNodes)
	assert.Equal(t, -1, walk.ReportedCachedTokens)
	assert.Equal(t, before.Stats.TotalRequests, after.Stats.TotalRequests)
	assert.Equal(t, before.Stats.Nodes, after.Stats.Nodes)
}

func TestWalkStopsAtAnEvictedNode(t *testing.T) {
	tree := New(1000, false)
	observe(t, tree, "r1", promptWith("a"), 300, -1)
	observe(t, tree, "r2", promptWith("a"), 300, 0)

	snapshot := tree.Snapshot(10)
	require.Positive(t, snapshot.Stats.EvictedNodes, "the setup must produce an eviction")
	assert.Empty(t, tree.Walk(promptWith("a")).ReusedNodes)
}

// The calibration is a measurement of this tokenizer on this traffic, not a
// constant.
func TestCharsPerTokenCalibratesFromReportedCounts(t *testing.T) {
	tree := New(1000, false)
	assert.InDelta(t, 4.0, tree.Snapshot(0).Stats.CharsPerToken, 1e-9,
		"the neutral default stands in until a request reports a token count")

	prompt := strings.Repeat("y", 1000)
	observe(t, tree, "r1", prompt, 500, -1)
	stats := tree.Snapshot(0).Stats
	assert.InDelta(t, 2.0, stats.CharsPerToken, 1e-9)
	assert.Equal(t, 1, stats.CalibrationN)
}

func TestShortfallBelowTheFloorIsNotAnEviction(t *testing.T) {
	tree := New(1000, false)
	observe(t, tree, "r1", promptWith("a"), 300, -1)
	// A few tokens of slack is this model's chunk granularity, never a drop.
	expected := tree.Walk(promptWith("a")).ExpectedCachedTokens
	_, evictions := observe(t, tree, "r2", promptWith("a"), 300, expected-2)

	assert.Empty(t, evictions)
	assert.Zero(t, tree.Snapshot(10).Stats.EvictedNodes)
}

func TestEvictionIsNoticedWhenTheServerReportsLess(t *testing.T) {
	tree := New(1000, false)
	observe(t, tree, "r1", promptWith("a"), 300, -1)
	match, evictions := observe(t, tree, "r2", promptWith("a"), 300, 0)

	require.Len(t, evictions, 1)
	ev := evictions[0]
	assert.Positive(t, ev.Tokens)
	assert.NotEmpty(t, ev.NodeIDs)
	assert.NotEmpty(t, ev.Evidence, "a reason without its evidence cannot be disagreed with")
	assert.Equal(t, "r2", ev.RequestID)
	assert.Equal(t, match.ExpectedCachedTokens, match.Shortfall)
}

func TestEvictionReasonReadsTheServerSignals(t *testing.T) {
	for name, tc := range map[string]struct {
		signals Signals
		reason  string
		conf    Confidence
	}{
		"restart": {
			Signals{MetricsOK: true, UpstreamRestarted: true}, "server_restart", Measured,
		},
		"full pool": {
			Signals{MetricsOK: true, TokenUsage: 0.95}, "capacity_pressure", Inferred,
		},
		"busy batch": {
			Signals{MetricsOK: true, TokenUsage: 0.75, RunningRequests: 3}, "contention_with_running_batch", Inferred,
		},
		"quiet server": {
			Signals{MetricsOK: true, TokenUsage: 0.1}, "unknown", Inferred,
		},
		"no metrics": {
			Signals{}, "unknown", Inferred,
		},
	} {
		t.Run(name, func(t *testing.T) {
			tree := New(1000, false)
			tree.SetSignals(func() Signals { return tc.signals })
			observe(t, tree, "r1", promptWith("a"), 300, -1)
			_, evictions := observe(t, tree, "r2", promptWith("a"), 300, 0)

			require.Len(t, evictions, 1)
			assert.Equal(t, tc.reason, evictions[0].Reason)
			assert.Equal(t, tc.conf, evictions[0].Confidence)
		})
	}
}

func TestABurstOfLossesReadsAsOneBulkEviction(t *testing.T) {
	tree := New(1000, false)
	tree.SetSignals(func() Signals { return Signals{MetricsOK: true, TokenUsage: 0.2} })

	// Separate roots: prefixes under a shared head stop matching after it is evicted.
	prompts := make([]string, 6)
	for i := range prompts {
		prompts[i] = strings.Repeat(string(rune('A'+i))+" distinct system prompt. ", 40)
	}
	for _, prompt := range prompts {
		observe(t, tree, "seed", prompt, 300, -1)
	}
	var last Eviction
	for _, prompt := range prompts {
		_, evictions := observe(t, tree, "again", prompt, 300, 0)
		if len(evictions) > 0 {
			last = evictions[0]
		}
	}
	assert.Equal(t, "cache_flush_or_bulk_eviction", last.Reason)
}

func TestAnEvictedPrefixComesBackWhenItIsReinserted(t *testing.T) {
	tree := New(1000, false)
	observe(t, tree, "r1", promptWith("a"), 300, -1)
	observe(t, tree, "r2", promptWith("a"), 300, 0)
	require.Positive(t, tree.Snapshot(10).Stats.EvictedNodes)

	match, _ := observe(t, tree, "r3", promptWith("a"), 300, -1)
	assert.NotEmpty(t, match.NewNodes, "the request re-inserts what the server dropped")
	assert.Zero(t, tree.Snapshot(10).Stats.EvictedNodes)
}

func TestRedactionWithholdsPromptText(t *testing.T) {
	tree := New(1000, true)
	observe(t, tree, "r1", promptWith("a secret question"), 300, -1)

	snapshot := tree.Snapshot(10)
	assert.True(t, snapshot.Redacted)
	for _, n := range snapshot.Nodes {
		assert.Empty(t, n.Preview)
		assert.Positive(t, n.Chars, "a redacted node still reports its size")
	}
}

// Dropping a node because the reconstruction is full is the dashboard
// forgetting, never the server evicting, so it publishes no eviction.
func TestPruningForgetsTheColdestNodesSilently(t *testing.T) {
	tree := New(64, false)
	for i := range 200 {
		observe(t, tree, "r", promptWith(strings.Repeat("q", i+1)), 300, -1)
	}
	stats := tree.Snapshot(10).Stats
	assert.LessOrEqual(t, stats.Nodes, 200)
	assert.Zero(t, stats.Evictions)
}

func TestMarkInFlightPinsAPath(t *testing.T) {
	tree := New(1000, false)
	match, _ := observe(t, tree, "r1", promptWith("a"), 300, -1)
	tree.MarkInFlight(match.Path, 1)

	pinned := 0
	for _, n := range tree.Snapshot(0).Nodes {
		if n.InFlight > 0 {
			pinned++
		}
	}
	assert.Equal(t, len(match.Path), pinned)

	// The counter floors at nothing pinned, so a stray release cannot wedge a node.
	tree.MarkInFlight(match.Path, -1)
	tree.MarkInFlight(match.Path, -1)
	for _, n := range tree.Snapshot(0).Nodes {
		assert.Zero(t, n.InFlight)
	}
}

func TestSnapshotOrdersTheLRUColdestFirst(t *testing.T) {
	tree := New(1000, false)
	observe(t, tree, "r1", promptWith("old"), 300, -1)
	time.Sleep(5 * time.Millisecond)
	observe(t, tree, "r2", promptWith("new"), 300, -1)

	snapshot := tree.Snapshot(10)
	require.NotEmpty(t, snapshot.LRU)
	byID := map[string]Node{}
	for _, n := range snapshot.Nodes {
		byID[n.ID] = n
	}
	for i := 1; i < len(snapshot.LRU); i++ {
		assert.LessOrEqual(t, byID[snapshot.LRU[i-1]].LastAccess, byID[snapshot.LRU[i]].LastAccess)
	}
}

// Every slice must be an array, even when empty.
func TestSnapshotNeverReturnsNilSlices(t *testing.T) {
	snapshot := New(1000, false).Snapshot(10)
	assert.NotNil(t, snapshot.Nodes)
	assert.NotNil(t, snapshot.Roots)
	assert.NotNil(t, snapshot.LRU)
	assert.NotNil(t, snapshot.Evictions)
}

func TestSnapshotCapsTheEvictionList(t *testing.T) {
	tree := New(4000, false)
	for i := range 12 {
		tail := strings.Repeat("z", i+1)
		observe(t, tree, "seed", promptWith(tail), 300, -1)
		observe(t, tree, "again", promptWith(tail), 300, 0)
	}
	assert.Len(t, tree.Snapshot(3).Evictions, 3)
	assert.Greater(t, len(tree.Snapshot(0).Evictions), 3, "a zero cap means no cap")
}

func TestStatsTrackHitsAgainstPromptTokens(t *testing.T) {
	tree := New(1000, false)
	observe(t, tree, "r1", promptWith("a"), 100, 0)
	observe(t, tree, "r2", promptWith("a"), 100, 60)

	stats := tree.Snapshot(0).Stats
	assert.Equal(t, 2, stats.TotalRequests)
	assert.Equal(t, 1, stats.TotalHits)
	assert.Equal(t, 60, stats.HitTokens)
	assert.Equal(t, 200, stats.PromptTokens)
	assert.InDelta(t, 0.3, stats.TokenHitRate, 1e-9)
}

func TestObserveIgnoresAnEmptyPrompt(t *testing.T) {
	tree := New(1000, false)
	match, evictions := observe(t, tree, "r1", "", 0, -1)
	assert.Empty(t, match.Path)
	assert.Empty(t, evictions)
	assert.Zero(t, tree.Snapshot(0).Stats.Nodes)
}
