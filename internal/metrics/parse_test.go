package metrics

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const exposition = `# HELP sglang:num_running_reqs Requests currently running
# TYPE sglang:num_running_reqs gauge
sglang:num_running_reqs{model_name="a/b",engine_type="x"} 3
sglang:num_running_reqs{model_name="c/d"} 4
# HELP sglang:token_usage KV pool fraction
# TYPE sglang:token_usage gauge
sglang:token_usage 0.75
# TYPE sglang:time_to_first_token_seconds histogram
sglang:time_to_first_token_seconds_bucket{le="0.1"} 5
sglang:time_to_first_token_seconds_bucket{le="1"} 8
sglang:time_to_first_token_seconds_bucket{le="+Inf"} 10
sglang:time_to_first_token_seconds_sum 4.5
sglang:time_to_first_token_seconds_count 10
`

func TestParseReadsLabelsAndValues(t *testing.T) {
	samples, err := Parse(strings.NewReader(exposition))
	require.NoError(t, err)

	byKey := map[string]Sample{}
	for _, s := range samples {
		byKey[s.Key()] = s
	}

	running := byKey[`sglang:num_running_reqs{engine_type=x,model_name=a/b}`]
	assert.Equal(t, 3.0, running.Value)
	assert.Equal(t, "gauge", running.Type)
	assert.Equal(t, "Requests currently running", running.Help)

	usage := byKey["sglang:token_usage"]
	assert.InDelta(t, 0.75, usage.Value, 1e-9)
}

// A histogram's HELP and TYPE are declared under the family name, so the
// bucket members have to look their metadata up there.
func TestParseGivesBucketsTheirFamilyType(t *testing.T) {
	samples, err := Parse(strings.NewReader(exposition))
	require.NoError(t, err)
	for _, s := range samples {
		if s.Name == "sglang:time_to_first_token_seconds_bucket" {
			assert.Equal(t, "histogram", s.Type)
			return
		}
	}
	t.Fatal("no bucket sample was parsed")
}

func TestParseReadsInfinity(t *testing.T) {
	samples, err := Parse(strings.NewReader("m_bucket{le=\"+Inf\"} 7\nn -Inf\n"))
	require.NoError(t, err)
	require.Len(t, samples, 2)
	assert.Equal(t, "+Inf", samples[0].Labels["le"])
	assert.True(t, math.IsInf(samples[1].Value, -1))
}

func TestParseIgnoresATrailingTimestamp(t *testing.T) {
	samples, err := Parse(strings.NewReader("m 12 1700000000000\n"))
	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.Equal(t, 12.0, samples[0].Value)
}

func TestParseHandlesEscapedLabelValues(t *testing.T) {
	samples, err := Parse(strings.NewReader(`m{msg="a\"b",path="c\\d"} 1` + "\n"))
	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.Equal(t, `a"b`, samples[0].Labels["msg"])
	assert.Equal(t, `c\d`, samples[0].Labels["path"])
}

// A line the parser cannot read must fail the whole scrape.
func TestParseRefusesAnUnreadableLine(t *testing.T) {
	for name, body := range map[string]string{
		"no value":           "m\n",
		"value not a number": "m abc\n",
		"unquoted label":     `m{a=1} 2` + "\n",
		"label without eq":   `m{a} 2` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(body))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "line 1")
		})
	}
}

func TestSampleKeyIsStableAcrossLabelOrder(t *testing.T) {
	a := Sample{Name: "m", Labels: map[string]string{"z": "1", "a": "2"}}
	b := Sample{Name: "m", Labels: map[string]string{"a": "2", "z": "1"}}
	assert.Equal(t, a.Key(), b.Key())
	assert.Equal(t, "m", Sample{Name: "m"}.Key())
	assert.Equal(t, a.Key(), FormatKey("m", map[string]string{"a": "2", "z": "1"}))
}

func TestParseFloat(t *testing.T) {
	v, ok := ParseFloat(" 1.5 ")
	assert.True(t, ok)
	assert.Equal(t, 1.5, v)
	_, ok = ParseFloat("nope")
	assert.False(t, ok)
}
