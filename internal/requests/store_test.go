package requests

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDerivedTimingsReportAbsenceRatherThanZero(t *testing.T) {
	inFlight := Record{ArrivedAt: 1000}
	assert.Equal(t, int64(-1), inFlight.TTFTMs())
	assert.Equal(t, int64(-1), inFlight.DecodeMs())
	assert.Equal(t, int64(-1), inFlight.TotalMs())
	assert.Zero(t, inFlight.OutputTokensPerSec())

	done := Record{ArrivedAt: 1000, FirstTokenAt: 1200, FinishedAt: 3200, CompletionTokens: 40}
	assert.Equal(t, int64(200), done.TTFTMs())
	assert.Equal(t, int64(2000), done.DecodeMs())
	assert.Equal(t, int64(2200), done.TotalMs())
	assert.InDelta(t, 20.0, done.OutputTokensPerSec(), 1e-9)
}

func TestOutputRateIsZeroWithoutTokens(t *testing.T) {
	r := Record{ArrivedAt: 1000, FirstTokenAt: 1200, FinishedAt: 3200}
	assert.Zero(t, r.OutputTokensPerSec())
}

func TestPutReplacesARecordInPlace(t *testing.T) {
	s := NewStore(16)
	s.Put(Record{ID: "a", Status: StatusInFlight, ArrivedAt: 1})
	s.Put(Record{ID: "a", Status: StatusDone, ArrivedAt: 1, FinishedAt: 2})

	got, ok := s.Get("a")
	require.True(t, ok)
	assert.Equal(t, StatusDone, got.Status)

	totals := s.Totals()
	assert.Equal(t, 1, totals.Seen, "a replacement is not a second request")
	assert.Equal(t, 1, totals.Retained)
	assert.Zero(t, totals.InFlight)
}

func TestRecentReturnsNewestFirst(t *testing.T) {
	s := NewStore(16)
	s.Put(Record{ID: "old", ArrivedAt: 100, Status: StatusDone})
	s.Put(Record{ID: "new", ArrivedAt: 200, Status: StatusDone})

	recent := s.Recent(0)
	require.Len(t, recent, 2)
	assert.Equal(t, "new", recent[0].ID)
	assert.Len(t, s.Recent(1), 1)
}

func TestTheRingForgetsTheOldestRecord(t *testing.T) {
	s := NewStore(16)
	for i := range 40 {
		s.Put(Record{ID: string(rune('a'+i%26)) + string(rune('0'+i/26)), ArrivedAt: int64(i), Status: StatusDone})
	}
	totals := s.Totals()
	assert.Equal(t, 40, totals.Seen)
	assert.Equal(t, 16, totals.Retained)
	assert.Len(t, s.Recent(0), 16)
}

func TestTotalsCountFailuresAndSlowRequests(t *testing.T) {
	s := NewStore(16)
	s.Put(Record{ID: "a", Status: StatusFailed, ArrivedAt: 1})
	s.Put(Record{ID: "b", Status: StatusDone, ArrivedAt: 2, Diagnosis: &Diagnosis{Slow: true}})
	s.Put(Record{ID: "c", Status: StatusInFlight, ArrivedAt: 3})

	totals := s.Totals()
	assert.Equal(t, 1, totals.Failed)
	assert.Equal(t, 1, totals.Slow)
	assert.Equal(t, 1, totals.InFlight)
}

func TestGetReportsAnUnknownID(t *testing.T) {
	_, ok := NewStore(16).Get("nothing")
	assert.False(t, ok)
}

func TestNewStoreClampsATinyCapacity(t *testing.T) {
	s := NewStore(1)
	for i := range 20 {
		s.Put(Record{ID: string(rune('a' + i)), ArrivedAt: int64(i), Status: StatusDone})
	}
	assert.Equal(t, 16, s.Totals().Retained)
}
