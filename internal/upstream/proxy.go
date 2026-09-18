// Package upstream proxies traffic to the SGLang server and scrapes its
// metrics. Proxying is what gives the dashboard per-request visibility: the
// server publishes aggregate metrics and no per-request feed, so the only
// place a prompt, its cached-token count and its earliest-token time exist
// together is on the wire.
package upstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wow-look-at-my/sglang-dash/internal/requests"
)

// Recorder receives what the proxy learns about each request.
type Recorder interface {
	// Started runs after the body is read, before it is forwarded. The prompt arrives separately.
	Started(r requests.Record, prompt string) requests.Record
	// FirstToken is called when the earliest response token reaches the client.
	FirstToken(id string, at time.Time)
	// Finished is called exactly a single time per request, on success and on failure.
	Finished(r requests.Record)
}

// Proxy forwards everything it is given to the SGLang server, recording the
// timings and token counts as it goes.
type Proxy struct {
	base     *url.URL
	client   *http.Client
	rec      Recorder
	redact   bool
	maxBody  int64
	sequence atomic.Uint64
}

// NewProxy builds a proxy. `maxBody` caps how much request body is buffered
// for inspection; a larger body is still forwarded in full, and the record
// says the prompt was truncated for analysis.
func NewProxy(base *url.URL, rec Recorder, redact bool, maxBody int64) *Proxy {
	if maxBody <= 0 {
		maxBody = 8 << 20
	}
	return &Proxy{
		base: base, rec: rec, redact: redact, maxBody: maxBody,
		client: &http.Client{
			Timeout: 0, // a generation can legitimately run for minutes
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				MaxIdleConnsPerHost: 64,
				IdleConnTimeout:     90 * time.Second,
				// An unbuffered transport keeps the reported TTFT the server's.
				DisableCompression:    true,
				ResponseHeaderTimeout: 0,
			},
		},
	}
}

func (p *Proxy) nextID() string {
	return fmt.Sprintf("r%d-%d", time.Now().UnixMilli(), p.sequence.Add(1))
}

// ServeHTTP forwards a single request.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	arrived := time.Now()
	id := p.nextID()

	body, readErr := io.ReadAll(io.LimitReader(r.Body, p.maxBody+1))
	r.Body.Close()
	truncated := int64(len(body)) > p.maxBody
	if truncated {
		body = body[:p.maxBody]
	}
	if readErr != nil {
		http.Error(w, "sglang-dash could not read the request body: "+readErr.Error(), http.StatusBadRequest)
		return
	}

	info := inspect(body)
	rec := requests.Record{
		ID: id, Path: r.URL.Path, Model: info.Model, Stream: info.Stream,
		Status:      requests.StatusInFlight,
		ArrivedAt:   arrived.UnixMilli(),
		PromptChars: len([]rune(info.Prompt)),
		// empty would read as a real miss.
		CachedTokens:    -1,
		PromptTruncated: truncated,
	}
	if !p.redact {
		rec.PromptPreview = head(info.Prompt, 400)
	}
	rec = p.rec.Started(rec, info.Prompt)

	target := *p.base
	target.Path = strings.TrimSuffix(p.base.Path, "/") + r.URL.Path
	target.RawQuery = r.URL.RawQuery

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		p.fail(rec, http.StatusInternalServerError, err)
		http.Error(w, "sglang-dash could not build the upstream request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	copyHeaders(outReq.Header, r.Header)
	outReq.Header.Del("Accept-Encoding")
	outReq.ContentLength = int64(len(body))

	rec.SentAt = time.Now().UnixMilli()

	resp, err := p.client.Do(outReq)
	if err != nil {
		p.fail(rec, http.StatusBadGateway, err)
		http.Error(w, "sglang-dash could not reach the SGLang server: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	rec.HTTPCode = resp.StatusCode

	flusher, _ := w.(http.Flusher)
	if isEventStream(resp.Header) {
		p.relayStream(w, flusher, resp.Body, &rec)
	} else {
		p.relayWhole(w, resp.Body, &rec)
	}

	rec.FinishedAt = time.Now().UnixMilli()
	if rec.Status == requests.StatusInFlight {
		if resp.StatusCode >= 400 {
			rec.Status = requests.StatusFailed
			rec.Error = "upstream returned " + resp.Status
		} else {
			rec.Status = requests.StatusDone
		}
	}
	p.rec.Finished(rec)
}

func (p *Proxy) fail(rec requests.Record, code int, err error) {
	rec.Status = requests.StatusFailed
	rec.HTTPCode = code
	rec.Error = err.Error()
	rec.FinishedAt = time.Now().UnixMilli()
	p.rec.Finished(rec)
}

// relayStream copies an SSE body through byte for byte while reading the usage
// block out of it. The client's stream is never held back for the dashboard's
// benefit: each chunk is written and flushed before it is parsed.
func (p *Proxy) relayStream(w io.Writer, flusher http.Flusher, body io.Reader, rec *requests.Record) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	sawToken := false
	for sc.Scan() {
		line := sc.Bytes()
		if _, err := w.Write(append(line, '\n')); err != nil {
			rec.Status = requests.StatusAborted
			rec.Error = "the client went away mid-stream: " + err.Error()
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		payload, ok := sseData(line)
		if !ok {
			continue
		}
		if !sawToken && hasContent(payload) {
			sawToken = true
			now := time.Now()
			rec.FirstTokenAt = now.UnixMilli()
			p.rec.FirstToken(rec.ID, now)
		}
		applyUsage(rec, payload)
	}
	if err := sc.Err(); err != nil {
		rec.Status = requests.StatusFailed
		rec.Error = "the upstream stream broke: " + err.Error()
	}
}

func (p *Proxy) relayWhole(w io.Writer, body io.Reader, rec *requests.Record) {
	buf, err := io.ReadAll(body)
	if err != nil {
		rec.Status = requests.StatusFailed
		rec.Error = "the upstream response broke: " + err.Error()
		return
	}
	if _, err := w.Write(buf); err != nil {
		rec.Status = requests.StatusAborted
		rec.Error = "the client went away before the response was written: " + err.Error()
		return
	}
	// A non-streamed response has no TTFT, so this stamp equals the finish time.
	now := time.Now()
	rec.FirstTokenAt = now.UnixMilli()
	p.rec.FirstToken(rec.ID, now)
	applyUsage(rec, buf)
}

type promptInfo struct {
	Prompt string
	Model  string
	Stream bool
}

// inspect pulls the prompt out of an OpenAI-shaped or a native SGLang body.
// A body it cannot read leaves the prompt empty, which shows in the UI as a
// request with no cache analysis rather than as a request with none needed.
func inspect(body []byte) promptInfo {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return promptInfo{}
	}
	info := promptInfo{}
	if v, ok := raw["model"]; ok {
		_ = json.Unmarshal(v, &info.Model)
	}
	if v, ok := raw["stream"]; ok {
		_ = json.Unmarshal(v, &info.Stream)
	}
	if v, ok := raw["messages"]; ok {
		info.Prompt = flattenMessages(v)
		return info
	}
	for _, key := range []string{"prompt", "text", "input_text"} {
		v, ok := raw[key]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil {
			info.Prompt = s
			return info
		}
		var list []string
		if json.Unmarshal(v, &list) == nil {
			info.Prompt = strings.Join(list, "\n")
			return info
		}
	}
	return info
}

func flattenMessages(raw json.RawMessage) string {
	var msgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return ""
	}
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Role)
		b.WriteString(": ")
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			b.WriteString(s)
		} else {
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(m.Content, &parts) == nil {
				for _, part := range parts {
					b.WriteString(part.Text)
				}
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

type usageBlock struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	Details          *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CachedTokens *int `json:"cached_tokens"`
}

// applyUsage reads whatever token counts the payload carries.
func applyUsage(rec *requests.Record, payload []byte) {
	var envelope struct {
		Usage *usageBlock `json:"usage"`
		Meta  *struct {
			CachedTokens   int `json:"cached_tokens"`
			PromptTokens   int `json:"prompt_tokens"`
			CompletionToks int `json:"completion_tokens"`
		} `json:"meta_info"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return
	}
	if u := envelope.Usage; u != nil {
		if u.PromptTokens > 0 {
			rec.PromptTokens = u.PromptTokens
		}
		if u.CompletionTokens > 0 {
			rec.CompletionTokens = u.CompletionTokens
		}
		switch {
		case u.Details != nil:
			rec.CachedTokens = u.Details.CachedTokens
		case u.CachedTokens != nil:
			rec.CachedTokens = *u.CachedTokens
		}
	}
	if m := envelope.Meta; m != nil {
		if m.PromptTokens > 0 {
			rec.PromptTokens = m.PromptTokens
		}
		if m.CompletionToks > 0 {
			rec.CompletionTokens = m.CompletionToks
		}
		if m.CachedTokens > 0 {
			rec.CachedTokens = m.CachedTokens
		}
	}
}

func sseData(line []byte) ([]byte, bool) {
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil, false
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return nil, false
	}
	return payload, true
}

// hasContent reports whether a streamed chunk carries generated text. A chunk
// that only opens the stream or sets a role does not stop the TTFT clock.
func hasContent(payload []byte) bool {
	var chunk struct {
		Choices []struct {
			Text  string `json:"text"`
			Delta *struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
		Text string `json:"text"`
	}
	if json.Unmarshal(payload, &chunk) != nil {
		return false
	}
	if chunk.Text != "" {
		return true
	}
	for _, c := range chunk.Choices {
		if c.Text != "" {
			return true
		}
		if c.Delta != nil && c.Delta.Content != "" {
			return true
		}
	}
	return false
}

func isEventStream(h http.Header) bool {
	return strings.Contains(strings.ToLower(h.Get("Content-Type")), "text/event-stream")
}

func copyHeaders(dst, src http.Header) {
	for k, vals := range src {
		switch strings.ToLower(k) {
		case "connection", "keep-alive", "transfer-encoding", "upgrade", "content-length":
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func head(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
