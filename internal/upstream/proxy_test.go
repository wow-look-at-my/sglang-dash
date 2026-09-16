package upstream

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wow-look-at-my/sglang-dash/internal/requests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recorder captures what the proxy reports, in order.
type recorder struct {
	mu         sync.Mutex
	started    []requests.Record
	prompts    []string
	firstToken []string
	finished   []requests.Record
}

func (r *recorder) Started(rec requests.Record, prompt string) requests.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, rec)
	r.prompts = append(r.prompts, prompt)
	return rec
}

func (r *recorder) FirstToken(id string, _ time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.firstToken = append(r.firstToken, id)
}

func (r *recorder) Finished(rec requests.Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finished = append(r.finished, rec)
}

func (r *recorder) lastFinished(t *testing.T) requests.Record {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotEmpty(t, r.finished, "the proxy must report every request exactly once")
	return r.finished[len(r.finished)-1]
}

func newProxy(t *testing.T, handler http.HandlerFunc) (*Proxy, *recorder, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	base, err := url.Parse(server.URL)
	require.NoError(t, err)
	rec := &recorder{}
	return NewProxy(base, rec, false, 1<<20), rec, server
}

func post(t *testing.T, p *Proxy, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	return w
}

const chatBody = `{"model":"m/1","stream":false,"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hello"}]}`

func TestTheProxyForwardsTheBodyUnchanged(t *testing.T) {
	var seen []byte
	var seenPath string
	p, _, _ := newProxy(t, func(w http.ResponseWriter, r *http.Request) {
		seen, _ = io.ReadAll(r.Body)
		seenPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Write([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":3}}`))
	})

	resp := post(t, p, "/v1/chat/completions?x=1", chatBody)
	assert.Equal(t, http.StatusOK, resp.Code)
	assert.Equal(t, chatBody, string(seen))
	assert.Equal(t, "/v1/chat/completions?x=1", seenPath)
}

func TestAChatPromptIsFlattenedForTheCacheModel(t *testing.T) {
	p, rec, _ := newProxy(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"usage":{"prompt_tokens":10}}`))
	})
	post(t, p, "/v1/chat/completions", chatBody)

	require.Len(t, rec.prompts, 1)
	assert.Contains(t, rec.prompts[0], "system: be brief")
	assert.Contains(t, rec.prompts[0], "user: hello")
	assert.Equal(t, "m/1", rec.started[0].Model)
	assert.False(t, rec.started[0].Stream)
}

func TestACompletionPromptIsReadFromItsOwnField(t *testing.T) {
	p, rec, _ := newProxy(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{}`))
	})
	post(t, p, "/v1/completions", `{"model":"m/1","prompt":"just a string"}`)
	assert.Equal(t, "just a string", rec.prompts[0])

	post(t, p, "/v1/completions", `{"prompt":["a","b"]}`)
	assert.Equal(t, "a\nb", rec.prompts[1])
}

func TestMultipartChatContentIsFlattened(t *testing.T) {
	p, rec, _ := newProxy(t, func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{}`)) })
	post(t, p, "/v1/chat/completions",
		`{"messages":[{"role":"user","content":[{"type":"text","text":"part one "},{"type":"text","text":"part two"}]}]}`)
	assert.Contains(t, rec.prompts[0], "part one part two")
}

// empty would read as a genuine miss.
func TestAnUnreportedCachedCountStaysAbsent(t *testing.T) {
	p, rec, _ := newProxy(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":2}}`))
	})
	post(t, p, "/v1/chat/completions", chatBody)

	got := rec.lastFinished(t)
	assert.Equal(t, -1, got.CachedTokens)
	assert.Equal(t, 10, got.PromptTokens)
	assert.Equal(t, 2, got.CompletionTokens)
}

func TestTheCachedCountIsReadFromEitherUsageShape(t *testing.T) {
	for name, body := range map[string]string{
		"openai details": `{"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":64}}}`,
		"flat field":     `{"usage":{"prompt_tokens":100,"completion_tokens":5,"cached_tokens":64}}`,
		"native meta":    `{"meta_info":{"prompt_tokens":100,"completion_tokens":5,"cached_tokens":64}}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, rec, _ := newProxy(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Write([]byte(body))
			})
			post(t, p, "/v1/chat/completions", chatBody)
			got := rec.lastFinished(t)
			assert.Equal(t, 64, got.CachedTokens)
			assert.Equal(t, 100, got.PromptTokens)
			assert.Equal(t, 5, got.CompletionTokens)
		})
	}
}

func TestAStreamIsRelayedByteForByteAndItsUsageRead(t *testing.T) {
	p, rec, _ := newProxy(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range []string{
			`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
			`data: {"choices":[{"delta":{"content":"he"}}]}`,
			`data: {"choices":[{"delta":{"content":"llo"}}]}`,
			`data: {"usage":{"prompt_tokens":80,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":70}}}`,
			"data: [DONE]",
		} {
			w.Write([]byte(line + "\n"))
		}
	})

	resp := post(t, p, "/v1/chat/completions", `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	body := resp.Body.String()
	assert.Contains(t, body, `"content":"he"`)
	assert.Contains(t, body, "[DONE]")

	got := rec.lastFinished(t)
	assert.Equal(t, 70, got.CachedTokens)
	assert.Equal(t, requests.StatusDone, got.Status)
	assert.Len(t, rec.firstToken, 1, "the first token is reported exactly once")
}

// A chunk that only opens the stream or sets a role does not stop the TTFT
// clock: reporting it would make every stream look instantaneous.
func TestARoleOnlyChunkDoesNotCountAsTheFirstToken(t *testing.T) {
	p, rec, _ := newProxy(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n"))
		w.Write([]byte("data: [DONE]\n"))
	})
	post(t, p, "/v1/chat/completions", `{"stream":true,"messages":[]}`)
	assert.Empty(t, rec.firstToken)
	assert.Zero(t, rec.lastFinished(t).FirstTokenAt)
}

func TestALegacyTextChunkCountsAsTheFirstToken(t *testing.T) {
	p, rec, _ := newProxy(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"text\":\"abc\"}]}\n"))
		w.Write([]byte("data: [DONE]\n"))
	})
	post(t, p, "/v1/chat/completions", `{"stream":true,"prompt":"x"}`)
	assert.Len(t, rec.firstToken, 1)
}

func TestAnUpstreamErrorIsReportedAsAFailure(t *testing.T) {
	p, rec, _ := newProxy(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model is loading", http.StatusServiceUnavailable)
	})
	resp := post(t, p, "/v1/chat/completions", chatBody)

	assert.Equal(t, http.StatusServiceUnavailable, resp.Code)
	got := rec.lastFinished(t)
	assert.Equal(t, requests.StatusFailed, got.Status)
	assert.Equal(t, http.StatusServiceUnavailable, got.HTTPCode)
	assert.Contains(t, got.Error, "503")
}

func TestAnUnreachableServerIsReportedAsABadGateway(t *testing.T) {
	base, err := url.Parse("http://127.0.0.1:1")
	require.NoError(t, err)
	rec := &recorder{}
	p := NewProxy(base, rec, false, 0)

	resp := post(t, p, "/v1/chat/completions", chatBody)
	assert.Equal(t, http.StatusBadGateway, resp.Code)
	assert.Contains(t, resp.Body.String(), "could not reach the SGLang server")
	assert.Equal(t, requests.StatusFailed, rec.lastFinished(t).Status)
}

func TestAnUnreadableBodyLeavesTheCacheModelNothingToChewOn(t *testing.T) {
	p, rec, _ := newProxy(t, func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{}`)) })
	post(t, p, "/v1/chat/completions", "this is not json")
	assert.Empty(t, rec.prompts[0], "an unparsed body means no prompt, never a wrong one")
}

func TestRedactionWithholdsThePromptPreview(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	require.NoError(t, err)
	rec := &recorder{}
	p := NewProxy(base, rec, true, 1<<20)

	post(t, p, "/v1/chat/completions", chatBody)
	assert.Empty(t, rec.started[0].PromptPreview)
	assert.Positive(t, rec.started[0].PromptChars, "the size is still reported")
}

// A body past the inspection cap is still forwarded whole. Only the analysis
// sees a truncated prompt, and the record says so.
func TestAnOversizedBodyIsForwardedWholeAndFlagged(t *testing.T) {
	long, err := json.Marshal(map[string]string{"prompt": strings.Repeat("x", 4096)})
	require.NoError(t, err)
	var seenLen int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenLen = len(body)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	require.NoError(t, err)
	rec := &recorder{}
	p := NewProxy(base, rec, false, 128)

	post(t, p, "/v1/completions", string(long))
	assert.True(t, rec.started[0].PromptTruncated)
	assert.Equal(t, 128, seenLen, "the cap bounds what is inspected and what is relayed")
}

func TestHopByHopHeadersAreNotCopied(t *testing.T) {
	p, _, _ := newProxy(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Model", "m/1")
		w.Write([]byte(`{}`))
	})
	resp := post(t, p, "/v1/chat/completions", chatBody)
	assert.Empty(t, resp.Header().Get("Connection"))
	assert.Equal(t, "m/1", resp.Header().Get("X-Model"))
}

func TestABasePathPrefixIsPreserved(t *testing.T) {
	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	base, err := url.Parse(server.URL + "/sglang")
	require.NoError(t, err)

	p := NewProxy(base, &recorder{}, false, 0)
	post(t, p, "/v1/chat/completions", chatBody)
	assert.Equal(t, "/sglang/v1/chat/completions", seenPath)
}
