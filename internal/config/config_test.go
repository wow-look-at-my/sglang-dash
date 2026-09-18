package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpstreamSetsTheDefaultMetricsURL(t *testing.T) {
	cfg, err := Parse([]string{"-upstream", "http://host:30000"})
	require.NoError(t, err)
	assert.Equal(t, "http://host:30000", cfg.Upstream.String())
	assert.Equal(t, "http://host:30000/metrics", cfg.MetricsURL.String())
	assert.Equal(t, ":8080", cfg.Listen)
	assert.Equal(t, time.Second, cfg.ScrapeEvery)
}

func TestAnUpstreamWithoutASchemeGetsOne(t *testing.T) {
	cfg, err := Parse([]string{"-upstream", "127.0.0.1:30000"})
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:30000", cfg.Upstream.String())
}

func TestAnExplicitMetricsURLWins(t *testing.T) {
	cfg, err := Parse([]string{"-upstream", "http://host:1", "-metrics-url", "http://other:2/m"})
	require.NoError(t, err)
	assert.Equal(t, "http://other:2/m", cfg.MetricsURL.String())
}

// An unconfigured dashboard refuses to start. A page of empty panels reads as
// a healthy, idle server.
func TestAnUnconfiguredDashboardRefusesToStart(t *testing.T) {
	_, err := Parse(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-upstream")
	assert.Contains(t, err.Error(), "-demo")
}

func TestDemoAndUpstreamAreMutuallyExclusive(t *testing.T) {
	_, err := Parse([]string{"-demo", "-upstream", "http://host:1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestDemoNeedsNoUpstream(t *testing.T) {
	cfg, err := Parse([]string{"-demo"})
	require.NoError(t, err)
	assert.True(t, cfg.Demo)
	assert.Nil(t, cfg.Upstream)
	assert.Nil(t, cfg.MetricsURL)
}

func TestARidiculousScrapeIntervalIsRefused(t *testing.T) {
	_, err := Parse([]string{"-demo", "-scrape-interval", "1ms"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 100ms")
}

func TestAnUpstreamWithoutAHostIsRefused(t *testing.T) {
	_, err := Parse([]string{"-upstream", "http:///nowhere"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-upstream")
}

func TestEnvironmentTwinsSupplyDefaults(t *testing.T) {
	t.Setenv("SGLANG_DASH_LISTEN", "127.0.0.1:9999")
	t.Setenv("SGLANG_DASH_DEMO", "true")
	t.Setenv("SGLANG_DASH_REDACT_PROMPTS", "yes")
	t.Setenv("SGLANG_DASH_MAX_REQUESTS", "42")
	t.Setenv("SGLANG_DASH_SLOW_FACTOR", "4")
	t.Setenv("SGLANG_DASH_SCRAPE_INTERVAL", "2s")

	cfg, err := Parse(nil)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:9999", cfg.Listen)
	assert.True(t, cfg.Demo)
	assert.True(t, cfg.RedactPrompts)
	assert.Equal(t, 42, cfg.MaxRequests)
	assert.InDelta(t, 4.0, cfg.SlowFactor, 1e-9)
	assert.Equal(t, 2*time.Second, cfg.ScrapeEvery)
}

func TestAFlagBeatsItsEnvironmentTwin(t *testing.T) {
	t.Setenv("SGLANG_DASH_LISTEN", "127.0.0.1:1111")
	cfg, err := Parse([]string{"-demo", "-listen", "127.0.0.1:2222"})
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:2222", cfg.Listen)
}

func TestAnUnknownFlagIsRefused(t *testing.T) {
	_, err := Parse([]string{"-nonsense"})
	require.Error(t, err)
}
