// Package cache reconstructs a view of the server's prefix cache from the
// traffic the dashboard proxies.
//
// It is a RECONSTRUCTION, not a readout. SGLang exposes no endpoint that lists
// the radix cache's nodes, so every structural fact here is derived from
// things the dashboard does see: the prompt text of each proxied request, and
// the usage block returned with it.
//
// Token counts the server reports are measurements. The tree shape, the token
// split and every eviction reason are inferences. The API and the UI label
// each claim with which of those it is.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"
)

// ChunkChars is the prefix granularity: it exposes a shared system prompt and keeps the tree small enough to draw.
const ChunkChars = 256

// Confidence labels how much of a claim is measured.
type Confidence string

const (
	// Measured means the server reported the number.
	Measured Confidence = "measured"
	// Derived means arithmetic over measured numbers.
	Derived Confidence = "derived"
	// Inferred means the dashboard reasoned about it and could be wrong.
	Inferred Confidence = "inferred"
)

// Node is a single prefix chunk shared by every request whose prompt starts with it.
type Node struct {
	ID       string `json:"id"`
	ParentID string `json:"parentId,omitempty"`
	Depth    int    `json:"depth"`
	// Hash identifies the chunk text; it is what makes prompts share.
	Hash string `json:"hash"`
	// Preview is the head of the chunk, for reading. Empty when redacting.
	Preview string `json:"preview"`
	Chars   int    `json:"chars"`
	// EstTokens is this chunk's token count at the calibrated ratio. Inferred.
	EstTokens int `json:"estTokens"`
	// PrefixTokens is the estimated token count of the whole path to here.
	PrefixTokens int `json:"prefixTokens"`

	Hits       int      `json:"hits"`
	Requests   int      `json:"requests"`
	CreatedAt  int64    `json:"createdAt"`
	LastAccess int64    `json:"lastAccess"`
	InFlight   int      `json:"inFlight"`
	Children   []string `json:"children"`
	// Evicted marks a node the dashboard believes the server dropped.
	Evicted   bool  `json:"evicted"`
	EvictedAt int64 `json:"evictedAt,omitempty"`
	// LastRequestID is the most recent request that touched this node.
	LastRequestID string `json:"lastRequestId,omitempty"`
}

// Match is what a single observation did to the tree.
type Match struct {
	Path []string `json:"path"`
	// ReusedNodes are the nodes that already existed when this prompt arrived.
	ReusedNodes []string `json:"reusedNodes"`
	// NewNodes are the nodes this prompt created.
	NewNodes []string `json:"newNodes"`
	// ExpectedCachedTokens is what the dashboard's tree predicted. Inferred.
	ExpectedCachedTokens int `json:"expectedCachedTokens"`
	// ReportedCachedTokens is what the server said.
	ReportedCachedTokens int `json:"reportedCachedTokens"`
	// Shortfall is expected minus reported. A positive a single is evidence of a drop.
	Shortfall int `json:"shortfall"`
}

// Eviction is a single believed loss of cached prefix.
type Eviction struct {
	Time       int64      `json:"time"`
	NodeIDs    []string   `json:"nodeIds"`
	Tokens     int        `json:"tokens"`
	Reason     string     `json:"reason"`
	Confidence Confidence `json:"confidence"`
	// Evidence carries the observations behind Reason, so a reader can disagree.
	Evidence  []string `json:"evidence"`
	RequestID string   `json:"requestId,omitempty"`
	Preview   string   `json:"preview,omitempty"`
}

// Stats summarises the whole reconstruction.
type Stats struct {
	Nodes          int     `json:"nodes"`
	LiveNodes      int     `json:"liveNodes"`
	EvictedNodes   int     `json:"evictedNodes"`
	DistinctRoots  int     `json:"distinctRoots"`
	TrackedTokens  int     `json:"trackedTokens"`
	TotalRequests  int     `json:"totalRequests"`
	TotalHits      int     `json:"totalHits"`
	HitTokens      int     `json:"hitTokens"`
	PromptTokens   int     `json:"promptTokens"`
	TokenHitRate   float64 `json:"tokenHitRate"`
	CharsPerToken  float64 `json:"charsPerToken"`
	CalibrationN   int     `json:"calibrationSamples"`
	Evictions      int     `json:"evictions"`
	EvictedTokens  int     `json:"evictedTokens"`
	LastEvictionAt int64   `json:"lastEvictionAt,omitempty"`
}

// Observation is a single proxied request, as far as the cache model cares.
type Observation struct {
	RequestID string
	Time      time.Time
	Prompt    string
	// PromptTokens and CachedTokens come from the response's usage block.
	PromptTokens int
	CachedTokens int
}

// Signals are the server-side numbers an eviction reason is classified against.
type Signals struct {
	TokenUsage      float64
	UsedTokens      float64
	RunningRequests float64
	// UpstreamRestarted is set when a monotonic counter went backwards.
	UpstreamRestarted bool
	MetricsOK         bool
}

// Tree is the reconstruction. Safe for concurrent use.
type Tree struct {
	mu      sync.RWMutex
	nodes   map[string]*Node
	roots   []string
	byHash  map[string]string // "<parent node id>/<chunk hash>" -> node id
	redact  bool
	maxNode int

	charsSeen  int
	tokensSeen int
	calibN     int

	evictions   []Eviction
	evictCap    int
	stats       Stats
	recentLoss  []int64 // times of recent eviction batches, for burst detection
	signalsFunc func() Signals
}

// New returns an empty tree. `maxNodes` caps the reconstruction; past it the
// coldest live nodes are dropped and reported as dashboard-side pruning, never
// as server evictions.
func New(maxNodes int, redactPrompts bool) *Tree {
	if maxNodes < 64 {
		maxNodes = 64
	}
	return &Tree{
		nodes:    map[string]*Node{},
		byHash:   map[string]string{},
		redact:   redactPrompts,
		maxNode:  maxNodes,
		evictCap: 500,
	}
}

// SetSignals supplies the server-side numbers a reason is classified against.
// Without it every eviction is classified "unknown".
func (t *Tree) SetSignals(f func() Signals) {
	t.mu.Lock()
	t.signalsFunc = f
	t.mu.Unlock()
}

// Chunks splits a prompt the way the tree indexes it.
func Chunks(prompt string) []string {
	r := []rune(prompt)
	if len(r) == 0 {
		return nil
	}
	out := make([]string, 0, len(r)/ChunkChars+1)
	for i := 0; i < len(r); i += ChunkChars {
		end := i + ChunkChars
		if end > len(r) {
			end = len(r)
		}
		out = append(out, string(r[i:end]))
	}
	return out
}

func hashChunk(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// charsPerToken is the calibrated ratio, or a neutral default until the
// earliest request has reported a token count.
func (t *Tree) charsPerToken() float64 {
	if t.calibN == 0 || t.tokensSeen == 0 {
		return 4.0
	}
	return float64(t.charsSeen) / float64(t.tokensSeen)
}

// Walk reports what a prompt WOULD match without changing anything. It is how
// an arriving request gets its prefix path before the server has said anything
// about it: folding the prompt in at arrival and again at completion would
// count every request again.
func (t *Tree) Walk(prompt string) Match {
	t.mu.RLock()
	defer t.mu.RUnlock()
	m := Match{ReportedCachedTokens: -1}
	parent := ""
	cpt := t.charsPerToken()
	for _, chunk := range Chunks(prompt) {
		key := parent + "/" + hashChunk(chunk)
		id, ok := t.byHash[key]
		if !ok {
			break
		}
		n := t.nodes[id]
		if n == nil || n.Evicted {
			break
		}
		m.Path = append(m.Path, id)
		m.ReusedNodes = append(m.ReusedNodes, id)
		est := int(float64(len([]rune(chunk)))/cpt + 0.5)
		if est < 1 {
			est = 1
		}
		m.ExpectedCachedTokens += est
		parent = id
	}
	return m
}

// Observe folds a single request into the tree and returns what it matched
// plus any eviction the observation revealed.
func (t *Tree) Observe(o Observation) (Match, []Eviction) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := o.Time.UnixMilli()
	chunks := Chunks(o.Prompt)
	if o.PromptTokens > 0 && len([]rune(o.Prompt)) > 0 {
		t.charsSeen += len([]rune(o.Prompt))
		t.tokensSeen += o.PromptTokens
		t.calibN++
	}
	cpt := t.charsPerToken()

	m := Match{ReportedCachedTokens: o.CachedTokens}
	parent := ""
	prefixTokens := 0
	reusedTokens := 0
	stillReusing := true

	for depth, chunk := range chunks {
		key := parent + "/" + hashChunk(chunk)
		id, exists := t.byHash[key]
		var n *Node
		if exists {
			n = t.nodes[id]
		}
		estTokens := int(float64(len([]rune(chunk)))/cpt + 0.5)
		if estTokens < 1 {
			estTokens = 1
		}
		prefixTokens += estTokens

		switch {
		case n == nil:
			id = key
			n = &Node{
				ID: id, ParentID: parent, Depth: depth, Hash: hashChunk(chunk),
				Chars: len([]rune(chunk)), EstTokens: estTokens, PrefixTokens: prefixTokens,
				CreatedAt: now,
			}
			if !t.redact {
				n.Preview = preview(chunk)
			}
			t.nodes[id] = n
			t.byHash[key] = id
			if parent == "" {
				t.roots = append(t.roots, id)
			} else if p := t.nodes[parent]; p != nil {
				p.Children = append(p.Children, id)
			}
			m.NewNodes = append(m.NewNodes, id)
			stillReusing = false
		case n.Evicted:
			// The server dropped it and this request re-inserts it.
			n.Evicted = false
			n.EvictedAt = 0
			n.CreatedAt = now
			m.NewNodes = append(m.NewNodes, id)
			stillReusing = false
		default:
			n.Hits++
			m.ReusedNodes = append(m.ReusedNodes, id)
			if stillReusing {
				reusedTokens += estTokens
			}
		}
		n.Requests++
		n.LastAccess = now
		n.LastRequestID = o.RequestID
		n.PrefixTokens = prefixTokens
		m.Path = append(m.Path, id)
		parent = id
	}

	m.ExpectedCachedTokens = reusedTokens
	t.stats.TotalRequests++
	t.stats.PromptTokens += o.PromptTokens
	if o.CachedTokens > 0 {
		t.stats.TotalHits++
		t.stats.HitTokens += o.CachedTokens
	}

	var evictions []Eviction
	if o.CachedTokens >= 0 {
		m.Shortfall = m.ExpectedCachedTokens - o.CachedTokens
		if ev, ok := t.detectEvictionLocked(o, m, now, cpt); ok {
			evictions = append(evictions, ev)
		}
	}

	t.pruneLocked()
	t.recomputeStatsLocked(cpt)
	return m, evictions
}

// pruneLocked keeps the reconstruction bounded. Dropping a node here is the
// dashboard forgetting, never the server evicting, so it publishes no eviction
// and the node is removed outright.
func (t *Tree) pruneLocked() {
	if len(t.nodes) <= t.maxNode {
		return
	}
	type cold struct {
		id   string
		last int64
	}
	var leaves []cold
	for id, n := range t.nodes {
		if len(n.Children) == 0 && n.InFlight == 0 {
			leaves = append(leaves, cold{id, n.LastAccess})
		}
	}
	sort.Slice(leaves, func(i, j int) bool { return leaves[i].last < leaves[j].last })
	drop := len(t.nodes) - t.maxNode
	for i := 0; i < len(leaves) && drop > 0; i++ {
		t.removeLocked(leaves[i].id)
		drop--
	}
}

func (t *Tree) removeLocked(id string) {
	n := t.nodes[id]
	if n == nil {
		return
	}
	delete(t.nodes, id)
	delete(t.byHash, id)
	if n.ParentID == "" {
		for i, r := range t.roots {
			if r == id {
				t.roots = append(t.roots[:i], t.roots[i+1:]...)
				break
			}
		}
		return
	}
	if p := t.nodes[n.ParentID]; p != nil {
		for i, c := range p.Children {
			if c == id {
				p.Children = append(p.Children[:i], p.Children[i+1:]...)
				break
			}
		}
	}
}

func (t *Tree) recomputeStatsLocked(cpt float64) {
	s := t.stats
	s.Nodes = len(t.nodes)
	s.DistinctRoots = len(t.roots)
	s.CharsPerToken = cpt
	s.CalibrationN = t.calibN
	s.LiveNodes, s.EvictedNodes, s.TrackedTokens = 0, 0, 0
	for _, n := range t.nodes {
		if n.Evicted {
			s.EvictedNodes++
			continue
		}
		s.LiveNodes++
		s.TrackedTokens += n.EstTokens
	}
	if s.PromptTokens > 0 {
		s.TokenHitRate = float64(s.HitTokens) / float64(s.PromptTokens)
	}
	t.stats = s
}

// Snapshot is the whole reconstruction, as the API serves it.
type Snapshot struct {
	Nodes     []Node     `json:"nodes"`
	Roots     []string   `json:"roots"`
	Stats     Stats      `json:"stats"`
	Evictions []Eviction `json:"evictions"`
	// LRU is the live nodes coldest earliest: the order an LRU drops them in. Inferred.
	LRU []string `json:"lru"`
	// Redacted reports whether prompt previews were withheld.
	Redacted bool `json:"redacted"`
}

// Snapshot copies the current state.
func (t *Tree) Snapshot(maxEvictions int) Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	stats := t.stats
	stats.CharsPerToken = t.charsPerToken()
	out := Snapshot{
		Stats: stats, Redacted: t.redact,
		Roots:     append(make([]string, 0, len(t.roots)), t.roots...),
		LRU:       make([]string, 0, len(t.nodes)),
		Evictions: make([]Eviction, 0, len(t.evictions)),
	}
	out.Nodes = make([]Node, 0, len(t.nodes))
	for _, n := range t.nodes {
		clone := *n
		clone.Children = append([]string(nil), n.Children...)
		out.Nodes = append(out.Nodes, clone)
	}
	sort.Slice(out.Nodes, func(i, j int) bool {
		if out.Nodes[i].Depth != out.Nodes[j].Depth {
			return out.Nodes[i].Depth < out.Nodes[j].Depth
		}
		return out.Nodes[i].ID < out.Nodes[j].ID
	})
	live := make([]Node, 0, len(out.Nodes))
	for _, n := range out.Nodes {
		if !n.Evicted {
			live = append(live, n)
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].LastAccess < live[j].LastAccess })
	for _, n := range live {
		out.LRU = append(out.LRU, n.ID)
	}
	keep := t.evictions
	if maxEvictions > 0 && len(keep) > maxEvictions {
		keep = keep[len(keep)-maxEvictions:]
	}
	out.Evictions = append(out.Evictions, keep...)
	return out
}

// MarkInFlight pins the nodes a request is currently using, so pruning does not
// forget a prefix that is being served right now.
func (t *Tree) MarkInFlight(path []string, delta int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, id := range path {
		if n := t.nodes[id]; n != nil {
			n.InFlight += delta
			if n.InFlight < 0 {
				n.InFlight = 0
			}
		}
	}
}

func preview(chunk string) string {
	s := strings.Join(strings.Fields(chunk), " ")
	r := []rune(s)
	if len(r) > 90 {
		return string(r[:90])
	}
	return s
}
