package cache

import "strconv"

// evictionTokenFloor is the slack below which a shortfall is this model's own chunk granularity, not a drop.
func (t *Tree) evictionTokenFloor(cpt float64) int {
	floor := int(float64(ChunkChars)/cpt + 0.5)
	if floor < 16 {
		floor = 16
	}
	return floor
}

func (t *Tree) detectEvictionLocked(o Observation, m Match, now int64, cpt float64) (Eviction, bool) {
	if m.Shortfall <= t.evictionTokenFloor(cpt) {
		return Eviction{}, false
	}
	// The server keeps prefixes and drops suffixes, so a shortfall points at the tail of the matched path.
	lost := 0
	var lostIDs []string
	for i := len(m.ReusedNodes) - 1; i >= 0 && lost < m.Shortfall; i-- {
		n := t.nodes[m.ReusedNodes[i]]
		if n == nil || n.Evicted {
			continue
		}
		n.Evicted = true
		n.EvictedAt = now
		lost += n.EstTokens
		lostIDs = append(lostIDs, n.ID)
	}
	if len(lostIDs) == 0 {
		return Eviction{}, false
	}

	var sig Signals
	if t.signalsFunc != nil {
		sig = t.signalsFunc()
	}
	reason, conf, evidence := classify(sig, m, lost, t.burstCountLocked(now))
	ev := Eviction{
		Time: now, NodeIDs: lostIDs, Tokens: lost, Reason: reason, Confidence: conf,
		Evidence: evidence, RequestID: o.RequestID,
	}
	if n := t.nodes[lostIDs[0]]; n != nil {
		ev.Preview = n.Preview
	}
	t.evictions = append(t.evictions, ev)
	if len(t.evictions) > t.evictCap {
		t.evictions = t.evictions[len(t.evictions)-t.evictCap:]
	}
	t.recentLoss = append(t.recentLoss, now)
	t.stats.Evictions++
	t.stats.EvictedTokens += lost
	t.stats.LastEvictionAt = now
	return ev, true
}

// burstWindowMs is how close believed evictions must be to read as a single server-side event.
const burstWindowMs = 10_000

func (t *Tree) burstCountLocked(now int64) int {
	keep := t.recentLoss[:0]
	for _, ts := range t.recentLoss {
		if now-ts <= burstWindowMs {
			keep = append(keep, ts)
		}
	}
	t.recentLoss = keep
	return len(keep)
}

func classify(sig Signals, m Match, lost, burst int) (string, Confidence, []string) {
	ev := []string{
		"expected " + itoa(m.ExpectedCachedTokens) + " cached tokens from the reconstructed tree",
		"server reported " + itoa(m.ReportedCachedTokens),
		"shortfall " + itoa(m.Shortfall) + " tokens, " + itoa(lost) + " attributed to dropped nodes",
	}
	switch {
	case sig.UpstreamRestarted:
		ev = append(ev, "an upstream counter read lower than the scrape before it")
		return "server_restart", Measured, ev
	case sig.MetricsOK && sig.TokenUsage >= 0.90:
		ev = append(ev, "token_usage was "+ftoa(sig.TokenUsage)+" at the time")
		return "capacity_pressure", Inferred, ev
	case burst >= 4:
		ev = append(ev, itoa(burst)+" prefixes were lost within "+itoa(burstWindowMs/1000)+"s")
		return "cache_flush_or_bulk_eviction", Inferred, ev
	case sig.MetricsOK && sig.RunningRequests >= 1 && sig.TokenUsage >= 0.70:
		ev = append(ev, "token_usage "+ftoa(sig.TokenUsage)+" with "+ftoa(sig.RunningRequests)+" running requests")
		return "contention_with_running_batch", Inferred, ev
	case !sig.MetricsOK:
		ev = append(ev, "no upstream metrics were available to classify against")
		return "unknown", Inferred, ev
	default:
		ev = append(ev, "token_usage was only "+ftoa(sig.TokenUsage)+", so capacity does not explain it")
		return "unknown", Inferred, ev
	}
}

func itoa(v int) string { return strconv.Itoa(v) }

func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
