// Package demo drives the dashboard from a built-in traffic simulator.
//
// Nothing here talks to a model server. It exists so the dashboard can be run,
// read and worked on without a GPU, and every screen it produces is labelled
// DEMO by the header strip.
package demo

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/wow-look-at-my/sglang-dash/internal/api"
	"github.com/wow-look-at-my/sglang-dash/internal/metrics"
	"github.com/wow-look-at-my/sglang-dash/internal/requests"
)

// Runner fabricates traffic and the metrics that would accompany it.
type Runner struct {
	hub *api.Hub

	// Requests run as goroutines, so every field below is only touched under mu.
	mu  sync.Mutex
	rng *rand.Rand

	// kvCapacity is the simulated KV pool in tokens, which drives the evictions.
	kvCapacity float64
	kvUsed     float64
	running    int
	queued     int

	promptTokens   float64
	genTokens      float64
	requestsTotal  float64
	abortedTotal   float64
	seq            int
	cachedPrefixes map[string]int
	// pace divides every simulated delay. It changes how fast the fabricated
	// traffic arrives and nothing about the code paths it drives.
	pace float64
}

// SetPace speeds the simulation up by the given factor. A test drives hours of
// traffic through the collectors in seconds with it.
func (r *Runner) SetPace(factor float64) {
	if factor <= 0 {
		factor = 1
	}
	r.mu.Lock()
	r.pace = factor
	r.mu.Unlock()
}

func (r *Runner) scaled(d time.Duration) time.Duration {
	r.mu.Lock()
	pace := r.pace
	r.mu.Unlock()
	if pace <= 1 {
		return d
	}
	return time.Duration(float64(d) / pace)
}

// New returns a runner seeded for reproducibility.
func New(hub *api.Hub, seed int64) *Runner {
	return &Runner{
		hub: hub, rng: rand.New(rand.NewSource(seed)),
		// The pool is small against the arrival rate on purpose: an eviction is what the dashboard exists to explain.
		kvCapacity: 11_000, cachedPrefixes: map[string]int{},
	}
}

var systemPrompts = []struct {
	name string
	body string
}{
	{"support-agent", "You are a support agent for an online bookstore. Always answer in a warm, concise register. Never invent an order number. If the customer asks about a refund, restate the 30-day policy verbatim before anything else. The catalogue covers fiction, non-fiction, childrens books and academic titles. Shipping is free above twenty pounds."},
	{"code-reviewer", "You are a meticulous code reviewer. Read the diff below and report only defects that change behaviour. Ignore formatting. For each finding give the file, the line, one sentence on the failure, and a concrete input that triggers it. Do not praise the change. Do not summarise what the diff does."},
	{"sql-analyst", "You translate questions into SQL against a warehouse with tables orders, customers, line_items and refunds. Always qualify column names. Never use SELECT *. Prefer a CTE over a nested subquery. Return the query alone, with no commentary, unless the question is ambiguous."},
	{"doc-summariser", "You summarise long technical documents. Produce exactly five bullets. Each bullet is one sentence. Keep every number, version and identifier the document states. Drop marketing language entirely. If the document contradicts itself, say so in the last bullet."},
}

var tailQuestions = []string{
	"Where is my order 4471 and why does the tracking say pending?",
	"Review the patch that moves the retry loop out of the request handler.",
	"How many customers refunded more than twice last quarter?",
	"Summarise the attached release notes for the storage layer.",
	"The customer says the parcel arrived damaged and wants a replacement.",
	"Explain why this join produces duplicate rows.",
	"What changed in the scheduler between the last two releases?",
	"Draft a reply declining the refund because the window closed.",
}

// Run drives the simulation until the context is cancelled.
func (r *Runner) Run(ctx context.Context) {
	metricsTick := time.NewTicker(time.Second)
	defer metricsTick.Stop()
	arrival := time.NewTimer(r.nextGap())
	defer arrival.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-metricsTick.C:
			r.publishMetrics()
		case <-arrival.C:
			go r.oneRequest(ctx)
			arrival.Reset(r.nextGap())
		}
	}
}

func (r *Runner) nextGap() time.Duration {
	r.mu.Lock()
	gap := time.Duration(150+r.rng.Intn(900)) * time.Millisecond
	pace := r.pace
	r.mu.Unlock()
	if pace > 1 {
		return time.Duration(float64(gap) / pace)
	}
	return gap
}

// publishMetrics feeds the metric store the exposition text a real server
// would have served, so the parser and every panel run the same path they run
// against a live SGLang.
func (r *Runner) publishMetrics() {
	r.mu.Lock()
	usage := r.kvUsed / r.kvCapacity
	if usage > 1 {
		usage = 1
	}
	text := fmt.Sprintf(`# HELP sglang:num_running_reqs Requests currently running
# TYPE sglang:num_running_reqs gauge
sglang:num_running_reqs{model_name="demo/simulated-8b"} %d
# HELP sglang:num_queue_reqs Requests waiting to be scheduled
# TYPE sglang:num_queue_reqs gauge
sglang:num_queue_reqs{model_name="demo/simulated-8b"} %d
# HELP sglang:token_usage Fraction of the KV pool in use
# TYPE sglang:token_usage gauge
sglang:token_usage{model_name="demo/simulated-8b"} %.4f
# HELP sglang:num_used_tokens Tokens held in the KV pool
# TYPE sglang:num_used_tokens gauge
sglang:num_used_tokens{model_name="demo/simulated-8b"} %.0f
# HELP sglang:gen_throughput Output tokens per second
# TYPE sglang:gen_throughput gauge
sglang:gen_throughput{model_name="demo/simulated-8b"} %.1f
# HELP sglang:cache_hit_rate Prefix cache hit rate reported by the server
# TYPE sglang:cache_hit_rate gauge
sglang:cache_hit_rate{model_name="demo/simulated-8b"} %.4f
# HELP sglang:prompt_tokens_total Prompt tokens processed
# TYPE sglang:prompt_tokens_total counter
sglang:prompt_tokens_total{model_name="demo/simulated-8b"} %.0f
# HELP sglang:generation_tokens_total Output tokens generated
# TYPE sglang:generation_tokens_total counter
sglang:generation_tokens_total{model_name="demo/simulated-8b"} %.0f
# HELP sglang:num_requests_total Requests accepted
# TYPE sglang:num_requests_total counter
sglang:num_requests_total{model_name="demo/simulated-8b"} %.0f
# HELP sglang:num_aborted_requests_total Requests aborted
# TYPE sglang:num_aborted_requests_total counter
sglang:num_aborted_requests_total{model_name="demo/simulated-8b"} %.0f
%s%s`,
		r.running, r.queued, usage, r.kvUsed, r.throughput(), r.hitRate(),
		r.promptTokens, r.genTokens, r.requestsTotal, r.abortedTotal,
		histogram("sglang:time_to_first_token_seconds", r.ttftBuckets()),
		histogram("sglang:e2e_request_latency_seconds", r.e2eBuckets()),
	)
	// The KV pool drains as finished requests release their blocks.
	r.kvUsed *= 0.97
	r.mu.Unlock()

	samples, err := metrics.Parse(strings.NewReader(text))
	if err != nil {
		// The simulator wrote this text, so a parse failure here is its own bug.
		r.hub.Events.Publish("dashboard.simulator_broken", "the simulator produced metrics it cannot parse: "+err.Error(), nil)
		return
	}
	r.hub.Metrics.Ingest(time.Now().UnixMilli(), samples)
}

func (r *Runner) throughput() float64 {
	if r.running == 0 {
		return 0
	}
	return float64(r.running) * (28 + r.rng.Float64()*14)
}

func (r *Runner) hitRate() float64 {
	if r.requestsTotal == 0 {
		return 0
	}
	hits := 0
	for _, n := range r.cachedPrefixes {
		if n > 1 {
			hits += n - 1
		}
	}
	return float64(hits) / r.requestsTotal
}

func (r *Runner) oneRequest(ctx context.Context) {
	r.mu.Lock()
	r.seq++
	seq := r.seq
	sys := systemPrompts[r.rng.Intn(len(systemPrompts))]
	tail := tailQuestions[r.rng.Intn(len(tailQuestions))]
	// A single request in is genuinely novel, so the tree keeps growing and
	// the cache does not read as a solved problem.
	if r.rng.Intn(8) == 0 {
		tail = fmt.Sprintf("%s (case %d, %s)", tail, seq, randomWords(r.rng, 40))
	}
	prompt := "system: " + sys.body + "\nuser: " + tail + "\n"
	promptTokens := len([]rune(prompt)) / 4

	hitBefore := r.cachedPrefixes[sys.name]
	cached := 0
	if hitBefore > 0 && r.kvUsed/r.kvCapacity < 0.97 {
		cached = len([]rune(sys.body)) / 4
	}
	r.cachedPrefixes[sys.name] = hitBefore + 1

	// Queue time grows with the running batch, prefill with the uncached tokens.
	queueMs := 8 + r.rng.Intn(30) + r.running*35
	prefillMs := (promptTokens-cached)/3 + 12
	if prefillMs < 5 {
		prefillMs = 5
	}
	if r.rng.Intn(20) == 0 {
		queueMs += 1200 + r.rng.Intn(2500)
	}
	outTokens := 40 + r.rng.Intn(400)
	decodeJitter := r.rng.Intn(8)
	r.queued++
	r.mu.Unlock()

	id := fmt.Sprintf("demo-%d", seq)
	arrived := time.Now()
	rec := requests.Record{
		ID: id, Path: "/v1/chat/completions", Model: "demo/simulated-8b", Stream: true,
		Status: requests.StatusInFlight, ArrivedAt: arrived.UnixMilli(),
		PromptChars: len([]rune(prompt)), CachedTokens: -1,
		PromptPreview: head(prompt, 400),
	}
	rec = r.hub.Started(rec, prompt)

	sleep(ctx, r.scaled(time.Duration(queueMs+prefillMs)*time.Millisecond))

	r.mu.Lock()
	r.queued--
	r.running++
	running := r.running
	r.mu.Unlock()

	rec.SentAt = arrived.Add(time.Millisecond).UnixMilli()
	first := time.Now()
	rec.FirstTokenAt = first.UnixMilli()
	r.hub.FirstToken(id, first)

	perTokenMs := 18 + running*4 + decodeJitter
	sleep(ctx, r.scaled(time.Duration(outTokens*perTokenMs/10)*time.Millisecond))

	rec.PromptTokens = promptTokens
	rec.CachedTokens = cached
	rec.CompletionTokens = outTokens
	rec.HTTPCode = 200
	rec.Status = requests.StatusDone
	rec.FinishedAt = time.Now().UnixMilli()

	r.mu.Lock()
	r.running--
	r.kvUsed += float64(promptTokens + outTokens)
	r.promptTokens += float64(promptTokens)
	r.genTokens += float64(outTokens)
	r.requestsTotal++
	// Under pressure the simulated server drops a shared prefix, which the detector has to notice on its own.
	if r.kvUsed/r.kvCapacity >= 0.97 {
		for name := range r.cachedPrefixes {
			delete(r.cachedPrefixes, name)
			break
		}
		// Only the dropped prefix's own blocks come back. A server that fell
		// to half-empty on every eviction would be out of pressure by the time
		// the next request observed the miss, and the dashboard would then
		// classify a capacity eviction as unexplained.
		r.kvUsed -= float64(evictedPrefixTokens)
		if r.kvUsed < 0 {
			r.kvUsed = 0
		}
	}
	r.mu.Unlock()

	r.hub.Finished(rec)
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func head(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

var vocabulary = strings.Fields(`ledger shard tenant invoice cursor batch replica quota token lattice
	gateway manifest checksum pipeline offset snapshot warehouse partition retry backlog
	envelope digest lease broker archive rollup sentinel fragment beacon kernel`)

func randomWords(rng *rand.Rand, n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = vocabulary[rng.Intn(len(vocabulary))]
	}
	return strings.Join(parts, " ")
}

func histogram(name string, buckets []bucketRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# TYPE %s histogram\n", name)
	total := 0.0
	for _, bk := range buckets {
		fmt.Fprintf(&b, "%s_bucket{le=%q} %.0f\n", name, bk.Le, bk.Count)
		total = bk.Count
	}
	fmt.Fprintf(&b, "%s_sum %.3f\n%s_count %.0f\n", name, total*0.4, name, total)
	return b.String()
}

// evictedPrefixTokens is roughly what a dropped system prompt held.
const evictedPrefixTokens = 900

// bucketRow is a single cumulative histogram bucket.
type bucketRow struct {
	Le    string
	Count float64
}

func (r *Runner) ttftBuckets() []bucketRow {
	base := r.requestsTotal
	return []bucketRow{
		{"0.05", base * 0.30}, {"0.1", base * 0.55}, {"0.25", base * 0.78},
		{"0.5", base * 0.90}, {"1", base * 0.96}, {"2.5", base * 0.99},
		{"5", base}, {"+Inf", base},
	}
}

func (r *Runner) e2eBuckets() []bucketRow {
	base := r.requestsTotal
	return []bucketRow{
		{"0.5", base * 0.20}, {"1", base * 0.45}, {"2.5", base * 0.75},
		{"5", base * 0.90}, {"10", base * 0.97}, {"30", base}, {"+Inf", base},
	}
}
