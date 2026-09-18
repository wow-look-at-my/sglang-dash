package events

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishStampsEachEvent(t *testing.T) {
	bus := NewBus(10)
	first := bus.Publish(KindCacheHit, "hit", map[string]any{"tokens": 5})
	second := bus.Publish(KindCacheMiss, "miss", nil)

	assert.Equal(t, uint64(1), first.Seq)
	assert.Equal(t, uint64(2), second.Seq)
	assert.Positive(t, first.Time)
	assert.Equal(t, KindCacheHit, first.Kind)
	assert.Equal(t, 5, first.Fields["tokens"])
}

func TestRecentReturnsOldestFirstWithinTheRing(t *testing.T) {
	bus := NewBus(3)
	for _, msg := range []string{"a", "b", "c", "d"} {
		bus.Publish(KindDashboardNotice, msg, nil)
	}
	recent := bus.Recent(0)
	require.Len(t, recent, 3, "the ring drops the oldest")
	assert.Equal(t, "b", recent[0].Message)
	assert.Equal(t, "d", recent[2].Message)

	assert.Len(t, bus.Recent(2), 2)
	assert.Equal(t, "d", bus.Recent(1)[0].Message)
}

func TestSubscribersReceiveEveryLaterEvent(t *testing.T) {
	bus := NewBus(10)
	bus.Publish(KindDashboardNotice, "before", nil)

	ch, stop := bus.Subscribe(4)
	bus.Publish(KindDashboardNotice, "after", nil)

	got := <-ch
	assert.Equal(t, "after", got.Message, "a subscriber sees the future, not the history")

	stop()
	_, open := <-ch
	assert.False(t, open, "the channel closes on unsubscribe")
}

// A full subscriber misses events instead of blocking. The dashboard must
// never hold up the proxy path to deliver its own telemetry.
func TestAFullSubscriberDropsRatherThanBlocks(t *testing.T) {
	bus := NewBus(100)
	ch, stop := bus.Subscribe(1)
	defer stop()

	for range 50 {
		bus.Publish(KindDashboardNotice, "flood", nil)
	}
	assert.Len(t, ch, 1)
	assert.Len(t, bus.Recent(0), 50, "the history keeps what the subscriber missed")
}

func TestStopIsSafeToCallTwice(t *testing.T) {
	bus := NewBus(4)
	_, stop := bus.Subscribe(1)
	stop()
	assert.NotPanics(t, stop)
}

func TestNewBusClampsATinyCapacity(t *testing.T) {
	bus := NewBus(0)
	bus.Publish(KindDashboardNotice, "a", nil)
	bus.Publish(KindDashboardNotice, "b", nil)
	recent := bus.Recent(0)
	require.Len(t, recent, 1)
	assert.Equal(t, "b", recent[0].Message)
}
