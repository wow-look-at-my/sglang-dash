// src/api.ts
async function getJSON(path) {
  const resp = await fetch(path, { headers: { accept: "application/json" } });
  if (!resp.ok) {
    throw new Error(`${path} returned ${resp.status} ${resp.statusText}: ${await resp.text()}`);
  }
  return await resp.json();
}
var api = {
  status: () => getJSON("/api/status"),
  events: (limit = 500) => getJSON(`/api/events?limit=${limit}`),
  requests: (limit = 300) => getJSON(`/api/requests?limit=${limit}`),
  cache: (evictions = 100) => getJSON(`/api/cache?evictions=${evictions}`)
};
function openFeed(handlers) {
  let source = null;
  let timer = 0;
  let closed = false;
  const connect = () => {
    if (closed) return;
    source = new EventSource("/api/stream");
    source.onopen = () => handlers.onConnectionChange(true, "live");
    source.onmessage = (ev) => {
      let payload;
      try {
        payload = JSON.parse(ev.data);
      } catch (err) {
        handlers.onConnectionChange(false, `unreadable frame: ${String(err)}`);
        return;
      }
      if (payload.type === "status" && payload.status) handlers.onStatus(payload.status);
      if (payload.type === "event" && payload.event) handlers.onEvent(payload.event);
    };
    source.onerror = () => {
      handlers.onConnectionChange(false, "reconnecting");
      source?.close();
      source = null;
      timer = window.setTimeout(connect, 1e3);
    };
  };
  connect();
  return () => {
    closed = true;
    window.clearTimeout(timer);
    source?.close();
  };
}

// src/cachepanel.ts
import "https://sites.pazer.build/js-snippets/branch/library/ui/dag-view.js";

// src/fmt.ts
var ABSENT = "\u2014";
function ms(value) {
  if (value === void 0 || value === null || !Number.isFinite(value) || value < 0) return ABSENT;
  if (value < 1e3) return `${Math.round(value)}ms`;
  if (value < 6e4) return `${(value / 1e3).toFixed(value < 1e4 ? 2 : 1)}s`;
  const mins = Math.floor(value / 6e4);
  return `${mins}m ${Math.round(value % 6e4 / 1e3)}s`;
}
function count(value) {
  if (value === void 0 || value === null || !Number.isFinite(value)) return ABSENT;
  if (Math.abs(value) < 1e3) return `${Math.round(value)}`;
  if (Math.abs(value) < 1e6) return `${(value / 1e3).toFixed(1)}k`;
  return `${(value / 1e6).toFixed(2)}M`;
}
function percent(fraction, digits = 0) {
  if (fraction === void 0 || fraction === null || !Number.isFinite(fraction)) return ABSENT;
  return `${(fraction * 100).toFixed(digits)}%`;
}
function age(at, now = Date.now()) {
  if (!at) return ABSENT;
  return `${ms(now - at)} ago`;
}
function clock(at) {
  if (!at) return ABSENT;
  const d = new Date(at);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${String(d.getMilliseconds()).padStart(3, "0")}`;
}
function pad(n) {
  return String(n).padStart(2, "0");
}
function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== void 0) node.textContent = text;
  return node;
}
function field(label, value, confidence) {
  const row = el("div", "field");
  row.append(el("span", "field-label", label), el("span", "field-value", value));
  if (confidence) row.append(el("span", `tag tag-${confidence}`, confidence));
  return row;
}

// src/cachepanel.ts
var MAX_DRAWN_NODES = 300;
var CachePanel = class {
  graph;
  summary;
  meter;
  meterFill;
  meterNote;
  lruList;
  evictionList;
  truncation;
  snapshot = null;
  constructor(root) {
    const left = el("div", "cache-left");
    this.summary = el("div", "cache-summary");
    this.meter = el("div", "meter");
    this.meterFill = el("div", "meter-fill");
    this.meter.append(this.meterFill);
    this.meterNote = el("div", "meter-note", "");
    this.truncation = el("div", "notice", "");
    this.graph = document.createElement("dag-view");
    this.graph.className = "cache-graph";
    this.graph.setAttribute("orientation", "LR");
    this.graph.setAttribute("empty-text", "No prompt has been observed yet, so there is no prefix tree to draw.");
    this.graph.tooltipFor = (hit) => this.nodeTooltip(hit);
    left.append(this.summary, this.meter, this.meterNote, this.truncation, this.graph);
    const right = el("div", "cache-right");
    right.append(
      sectionTitle("closest to eviction", "coldest first \u2014 this is the order the server's LRU would drop them in")
    );
    this.lruList = el("div", "lru-list");
    right.append(this.lruList);
    right.append(sectionTitle("what was lost, and why", "each row carries the evidence the reason was drawn from"));
    this.evictionList = el("div", "eviction-list");
    right.append(this.evictionList);
    root.append(left, right);
  }
  update(snapshot, status) {
    this.snapshot = snapshot;
    this.renderSummary(snapshot, status);
    this.renderMeter(status);
    this.renderGraph(snapshot);
    this.renderLRU(snapshot);
    this.renderEvictions(snapshot.evictions);
  }
  renderSummary(s, status) {
    this.summary.replaceChildren(
      field("live prefix chunks", count(s.stats.liveNodes), "inferred"),
      field("tokens tracked", count(s.stats.trackedTokens), "inferred"),
      field("distinct prompt roots", count(s.stats.distinctRoots), "inferred"),
      field(
        "prompt tokens served from cache",
        s.stats.promptTokens > 0 ? `${count(s.stats.hitTokens)} of ${count(s.stats.promptTokens)}` : ABSENT,
        "measured"
      ),
      field("token hit rate, as observed here", percent(s.stats.tokenHitRate, 1), "measured"),
      field(
        "server's own hit rate",
        status.metrics.ok && status.metrics.gauges.cache_hit_rate !== void 0 ? percent(status.metrics.gauges.cache_hit_rate, 1) : "not exported by this server",
        "measured"
      ),
      field(
        "chars per token, calibrated",
        s.stats.calibrationSamples > 0 ? `${s.stats.charsPerToken.toFixed(2)} over ${count(s.stats.calibrationSamples)} requests` : "no request has reported a token count yet",
        "derived"
      ),
      field("believed evictions", `${count(s.stats.evictions)} (~${count(s.stats.evictedTokens)} tokens)`, "inferred")
    );
    if (s.redacted) {
      this.summary.append(
        field("prompt text", "withheld: the dashboard was started with -redact-prompts", "measured")
      );
    }
  }
  renderMeter(status) {
    const usage = status.metrics.ok ? status.metrics.gauges.token_usage : void 0;
    if (usage === void 0 || !Number.isFinite(usage)) {
      this.meter.classList.add("meter-absent");
      this.meterFill.style.width = "0%";
      this.meterNote.textContent = status.metrics.ok ? "this server does not export token_usage, so how full the KV pool is cannot be read from here" : "the metrics scrape is failing, so pool occupancy is unknown";
      return;
    }
    this.meter.classList.remove("meter-absent");
    const pct = Math.max(0, Math.min(1, usage));
    this.meterFill.style.width = `${(pct * 100).toFixed(1)}%`;
    this.meter.dataset.level = pct >= 0.9 ? "critical" : pct >= 0.7 ? "warn" : "ok";
    this.meterNote.textContent = `KV pool ${percent(usage, 1)} full \u2014 ${count(status.metrics.gauges.used_tokens)} tokens held. ` + (pct >= 0.9 ? "At this level the server is evicting cached prefixes to admit new work." : "The cached prefixes below are safe while there is room.");
  }
  renderGraph(s) {
    const ranked = [...s.nodes].sort((a, b) => b.lastAccess - a.lastAccess);
    const drawn = ranked.slice(0, MAX_DRAWN_NODES);
    const ids = new Set(drawn.map((n) => n.id));
    const rootOf = rootResolver(s.nodes);
    const nodes = drawn.map((n) => ({
      id: n.id,
      label: n.preview ? shorten(n.preview, 46) : `chunk ${n.hash}`,
      sublabel: `${count(n.estTokens)} tok \xB7 ${count(n.hits)} hits \xB7 depth ${n.depth}`,
      category: rootOf(n),
      state: n.evicted ? "missing" : n.inFlight > 0 ? "blocked" : "done"
    }));
    const edges = drawn.filter((n) => n.parentId && ids.has(n.parentId)).map((n) => ({ from: n.parentId, to: n.id }));
    this.graph.setData({ nodes, edges });
    this.truncation.textContent = s.nodes.length > drawn.length ? `Drawing the ${drawn.length} most recently used chunks of ${s.nodes.length}. The rest are still tracked and still counted above.` : "";
    this.truncation.classList.toggle("hidden", s.nodes.length <= drawn.length);
  }
  renderLRU(s) {
    const byID = new Map(s.nodes.map((n) => [n.id, n]));
    const rows = s.lru.slice(0, 12).map((id, index) => {
      const n = byID.get(id);
      const row = el("div", "lru-row");
      if (!n) {
        row.append(el("div", "lru-preview", "this chunk left the reconstruction before it could be listed"));
        return row;
      }
      row.append(el("div", "lru-rank", `#${index + 1}`));
      const body = el("div", "lru-body");
      body.append(el("div", "lru-preview", n.preview || `chunk ${n.hash}`));
      body.append(
        el(
          "div",
          "lru-meta",
          `idle ${age(n.lastAccess)} \xB7 ${count(n.estTokens)} tokens \xB7 ${count(n.hits)} hits \xB7 ${n.inFlight > 0 ? "pinned by a live request" : "droppable"}`
        )
      );
      row.append(body);
      return row;
    });
    this.lruList.replaceChildren(
      ...rows.length > 0 ? rows : [el("div", "empty", "Nothing is cached yet. The first prompt through the proxy starts the tree.")]
    );
  }
  renderEvictions(evictions) {
    if (evictions.length === 0) {
      this.evictionList.replaceChildren(
        el(
          "div",
          "empty",
          "No prefix loss has been observed. An eviction is noticed when a prompt whose prefix this tree still holds comes back reporting fewer cached tokens than it should."
        )
      );
      return;
    }
    const rows = [...evictions].reverse().slice(0, 20).map((ev) => {
      const row = el("div", "eviction-row");
      const head = el("div", "eviction-head");
      head.append(
        el("span", `reason reason-${ev.reason}`, ev.reason.replace(/_/g, " ")),
        el("span", `tag tag-${ev.confidence}`, ev.confidence),
        el("span", "eviction-when", clock(ev.time))
      );
      row.append(head);
      row.append(
        el("div", "eviction-body", `~${count(ev.tokens)} tokens across ${count(ev.nodeIds.length)} chunks`)
      );
      if (ev.preview) row.append(el("div", "eviction-preview", shorten(ev.preview, 110)));
      const evidence = el("ul", "evidence");
      for (const line of ev.evidence) evidence.append(el("li", "", line));
      row.append(evidence);
      return row;
    });
    this.evictionList.replaceChildren(...rows);
  }
  nodeTooltip(hit) {
    const s = this.snapshot;
    if (!hit.node || !s) return null;
    const n = s.nodes.find((candidate) => candidate.id === hit.node?.id);
    if (!n) return null;
    const rank = s.lru.indexOf(n.id);
    return {
      title: n.preview || `chunk ${n.hash}`,
      rows: [
        ["estimated tokens", count(n.estTokens)],
        ["tokens in the whole prefix", count(n.prefixTokens)],
        ["requests through here", count(n.requests)],
        ["reuses", count(n.hits)],
        ["first seen", clock(n.createdAt)],
        ["last used", `${clock(n.lastAccess)} (${age(n.lastAccess)})`],
        ["lifetime so far", ms(Date.now() - n.createdAt)],
        ["eviction rank", rank < 0 ? "not in the live order" : `#${rank + 1} of ${s.lru.length}, coldest first`],
        ["pinned", n.inFlight > 0 ? `${count(n.inFlight)} live requests` : "no"],
        ["state", n.evicted ? "believed dropped by the server" : "believed cached"]
      ]
    };
  }
};
function rootResolver(nodes) {
  const byID = new Map(nodes.map((n) => [n.id, n]));
  const memo = /* @__PURE__ */ new Map();
  return (node) => {
    const seen = [];
    let current = node;
    let root = node.hash;
    while (current) {
      const hit = memo.get(current.id);
      if (hit !== void 0) {
        root = hit;
        break;
      }
      seen.push(current.id);
      if (!current.parentId) {
        root = current.hash;
        break;
      }
      const parent = byID.get(current.parentId);
      if (!parent) {
        root = current.hash;
        break;
      }
      current = parent;
    }
    for (const id of seen) memo.set(id, root);
    return root;
  };
}
function shorten(text, max) {
  return text.length <= max ? text : `${text.slice(0, max - 1)}\u2026`;
}
function sectionTitle(title, note) {
  const head = el("div", "section-title");
  head.append(el("h3", "", title), el("p", "section-note", note));
  return head;
}

// src/feed.ts
import "https://sites.pazer.build/js-snippets/branch/library/ui/activity-feed.js";
var Feed = class {
  view;
  events = [];
  cap;
  constructor(root, cap = 2e3) {
    this.cap = cap;
    this.view = document.createElement("activity-feed");
    this.view.className = "feed";
    this.view.setAttribute("family-chips", "");
    this.view.setAttribute("storage-key", "sglang-dash.feed");
    this.view.setAttribute("placeholder", "filter events\u2026");
    this.view.emptyText = "Nothing has happened yet.";
    this.view.messageRenderer = (message, entry) => renderMessage(message, entry);
    root.append(this.view);
  }
  replace(events) {
    this.events = events.slice(-this.cap);
    this.render();
  }
  add(event) {
    this.events.push(event);
    if (this.events.length > this.cap) this.events = this.events.slice(-this.cap);
    this.render();
  }
  all() {
    return this.events;
  }
  render() {
    this.view.entries = [...this.events].reverse().map((e) => ({ time: e.time, kind: e.kind, message: e.message, ...e.fields ?? {} }));
  }
};
function renderMessage(message, entry) {
  const evidence = entry.evidence;
  if (!Array.isArray(evidence) || evidence.length === 0) return message;
  const box = el("div", "feed-message");
  box.append(el("div", "", message));
  const list = el("ul", "evidence");
  for (const line of evidence) list.append(el("li", "", String(line)));
  box.append(list);
  return box;
}

// src/gauges.ts
import "https://sites.pazer.build/js-snippets/branch/library/ui/perf-graph.js";
var SPECS = [
  {
    key: "running_reqs",
    label: "running",
    unit: "",
    min: 0,
    read: (s) => gauge(s, "running_reqs"),
    hint: "requests the server is decoding right now"
  },
  {
    key: "queued_reqs",
    label: "queued",
    unit: "",
    min: 0,
    read: (s) => gauge(s, "queued_reqs"),
    hint: "requests admitted but not yet scheduled \u2014 the first place latency hides"
  },
  {
    key: "token_usage",
    label: "KV usage",
    unit: "%",
    min: 0,
    max: 100,
    budget: 90,
    read: (s) => {
      const v = gauge(s, "token_usage");
      return v === null ? null : v * 100;
    },
    hint: "how full the KV pool is; past the dashed line the server starts evicting cached prefixes"
  },
  {
    key: "gen_throughput",
    label: "output",
    unit: "tok/s",
    min: 0,
    read: (s) => gauge(s, "gen_throughput"),
    hint: "generation throughput across the whole batch"
  },
  {
    key: "cache_hit_rate",
    label: "cache hit",
    unit: "%",
    min: 0,
    max: 100,
    read: (s) => {
      const v = gauge(s, "cache_hit_rate");
      return v === null ? null : v * 100;
    },
    hint: "the server's own prefix-cache hit rate, not the dashboard's reconstruction"
  },
  {
    key: "ttft_p50",
    label: "TTFT p50",
    unit: "ms",
    min: 0,
    read: (s) => quantile(s, "ttft", "p50"),
    hint: "time to first token at the median, off the server's histogram"
  },
  {
    key: "ttft_p99",
    label: "TTFT p99",
    unit: "ms",
    min: 0,
    read: (s) => quantile(s, "ttft", "p99"),
    hint: "the tail that a user actually complains about"
  },
  {
    key: "e2e_p99",
    label: "e2e p99",
    unit: "ms",
    min: 0,
    read: (s) => quantile(s, "e2e_latency", "p99"),
    hint: "whole-request latency at the tail"
  }
];
function gauge(status, field2) {
  if (!status.metrics.ok) return null;
  const v = status.metrics.gauges[field2];
  return v === void 0 || !Number.isFinite(v) ? null : v;
}
function quantile(status, family, q) {
  if (!status.metrics.ok) return null;
  const row = status.metrics.quantiles[family];
  if (!row || !Number.isFinite(row[q])) return null;
  return row[q] * 1e3;
}
var Gauges = class {
  mounted = [];
  constructor(root) {
    for (const spec of SPECS) {
      const card = el("div", "gauge-card");
      const graph = document.createElement("perf-graph");
      graph.setAttribute("label", spec.label);
      graph.setAttribute("unit", spec.unit);
      graph.setAttribute("history", "240");
      graph.setAttribute("height", "64");
      if (spec.min !== void 0) graph.setAttribute("min", String(spec.min));
      if (spec.max !== void 0) graph.setAttribute("max", String(spec.max));
      if (spec.budget !== void 0) graph.setAttribute("budget", String(spec.budget));
      const missing = el("div", "gauge-missing", "");
      card.append(graph, missing, el("div", "gauge-hint", spec.hint));
      root.append(card);
      this.mounted.push({ spec, graph, card, missing });
    }
  }
  update(status) {
    for (const m of this.mounted) {
      const value = m.spec.read(status);
      if (value === null) {
        m.card.classList.add("gauge-absent");
        m.missing.textContent = status.metrics.ok ? `${m.spec.key} is not exported by this server` : "the metrics scrape is failing, so this gauge has nothing to draw";
        continue;
      }
      m.card.classList.remove("gauge-absent");
      m.missing.textContent = "";
      m.graph.push(value);
    }
  }
};

// src/header.ts
var Header = class {
  constructor(root) {
    this.root = root;
    const left = el("div", "header-identity");
    this.mode = el("span", "mode-badge", "starting");
    this.target = el("span", "header-target", "");
    left.append(el("span", "product", "sglang-dash"), this.mode, this.target);
    this.link = el("span", "link-state link-unknown", "connecting");
    const right = el("div", "header-stats");
    for (const [key, label] of [
      ["running", "running"],
      ["queued", "queued"],
      ["kv", "KV used"],
      ["throughput", "output"],
      ["hit", "cache hit"],
      ["ttft", "TTFT p50/p99"],
      ["inflight", "in flight"],
      ["slow", "slow"]
    ]) {
      const node = el("div", "stat");
      const value = el("div", "stat-value", ABSENT);
      const note = el("div", "stat-note", "");
      node.append(el("div", "stat-label", label), value, note);
      right.append(node);
      this.stats.set(key, { node, value, note });
    }
    this.root.append(left, this.link, right);
  }
  mode;
  target;
  link;
  stats = /* @__PURE__ */ new Map();
  setConnection(connected, detail) {
    this.link.textContent = detail;
    this.link.className = `link-state ${connected ? "link-live" : "link-down"}`;
  }
  update(status) {
    this.mode.textContent = status.mode;
    this.mode.className = `mode-badge mode-${status.mode}`;
    this.target.textContent = status.mode === "demo" ? "simulated traffic \u2014 nothing on this screen came from a model server" : `observing ${status.upstream ?? ABSENT}`;
    const g = status.metrics.gauges;
    const ok = status.metrics.ok;
    const scrapeNote = ok ? "" : `metrics scrape failing: ${status.metrics.error ?? "unknown"}`;
    this.set("running", ok ? count(g.running_reqs) : ABSENT, scrapeNote || "on the server now");
    this.set("queued", ok ? count(g.queued_reqs) : ABSENT, scrapeNote || "waiting to be scheduled");
    this.set("kv", ok ? percent(g.token_usage) : ABSENT, ok ? `${count(g.used_tokens)} tokens held` : scrapeNote);
    this.set("throughput", ok ? `${count(g.gen_throughput)} tok/s` : ABSENT, scrapeNote || "generation throughput");
    this.set(
      "hit",
      ok && g.cache_hit_rate !== void 0 ? percent(g.cache_hit_rate, 1) : ABSENT,
      ok && g.cache_hit_rate === void 0 ? "this server does not report it" : "as the server reports it"
    );
    const ttft2 = status.metrics.quantiles.ttft;
    this.set(
      "ttft",
      ttft2 ? `${ms(ttft2.p50 * 1e3)} / ${ms(ttft2.p99 * 1e3)}` : ABSENT,
      ttft2 ? `over ${count(ttft2.count)} requests` : "no TTFT histogram on this server"
    );
    this.set("inflight", count(status.requests.inFlight), `${count(status.requests.seen)} proxied since start`);
    this.set(
      "slow",
      count(status.requests.slow),
      status.baseline.samples >= 5 ? `median ${ms(status.baseline.medianTotalMs)}` : `baseline needs ${5 - status.baseline.samples} more requests`
    );
    this.root.classList.toggle("degraded", !ok);
  }
  set(key, value, note) {
    const stat2 = this.stats.get(key);
    if (!stat2) return;
    stat2.value.textContent = value;
    stat2.note.textContent = note;
    stat2.node.classList.toggle("absent", value === ABSENT);
  }
};

// src/requeststable.ts
import "https://sites.pazer.build/js-snippets/branch/library/ui/data-table.js";
var RequestsTable = class {
  table;
  records = [];
  constructor(root) {
    this.table = document.createElement("data-table");
    this.table.className = "requests-table";
    this.table.setAttribute("searchable", "");
    this.table.setAttribute("placeholder", "filter by id, model, prompt text, error\u2026");
    this.table.setAttribute("storage-key", "sglang-dash.requests");
    this.table.emptyText = "No request has been proxied yet.";
    this.table.rowId = (r) => r.id;
    this.table.rowClass = (r) => r.status === "failed" ? "row-failed" : r.diagnosis?.slow ? "row-slow" : "";
    this.table.searchText = (r) => [r.id, r.model, r.path, r.status, r.error ?? "", r.promptPreview ?? ""].join(" ");
    this.table.columns = columns();
    this.table.facets = [
      {
        key: "status",
        label: "status",
        of: (r) => r.status,
        order: ["in_flight", "done", "failed", "aborted"]
      },
      {
        key: "speed",
        label: "speed",
        of: (r) => r.diagnosis?.slow ? "slow" : r.finishedAt ? "normal" : "running",
        order: ["slow", "normal", "running"]
      },
      {
        key: "cache",
        label: "prefix cache",
        of: (r) => cacheBucket(r),
        order: ["mostly cached", "partly cached", "not cached", "unreported"]
      },
      { key: "model", label: "model", of: (r) => r.model || "unnamed" }
    ];
    this.table.detailFor = (r) => this.detail(r);
    this.table.styleText = DETAIL_STYLES;
    root.append(this.table);
  }
  setRequests(records) {
    this.records = records;
    this.table.rows = records;
  }
  /** Opens a single request's detail, used when a timeline bar is clicked. */
  reveal(id) {
    const row = this.records.find((r) => r.id === id);
    if (!row) return;
    this.table.expanded = [id];
    this.table.scrollIntoView({ behavior: "smooth", block: "center" });
  }
  detail(r) {
    const box = el("div", "detail");
    const total2 = span(r.finishedAt, r.arrivedAt);
    const head = el("div", "detail-head");
    head.append(
      stat("total", ms(total2)),
      stat("to first token", r.stream ? ms(span(r.firstTokenAt, r.arrivedAt)) : "not streamed"),
      stat("prompt", `${count(r.promptTokens)} tok`),
      stat(
        "from cache",
        r.cachedTokens < 0 ? "unreported" : `${count(r.cachedTokens)} tok \xB7 ${cachedShare(r)}`
      ),
      stat("output", `${count(r.completionTokens)} tok`),
      stat("decode rate", decodeRate(r))
    );
    box.append(head);
    const d = r.diagnosis;
    if (!d) {
      box.append(
        el(
          "p",
          "detail-note",
          r.status === "in_flight" ? "This request is still running, so there is nothing to attribute yet." : "This request finished without a diagnosis."
        )
      );
    } else {
      box.append(
        el(
          "p",
          "detail-note",
          d.slow ? `Flagged slow: ${ms(total2)} against a ${ms(d.thresholdMs)} threshold.` : `Within normal range: ${ms(total2)} against a ${ms(d.thresholdMs)} threshold.`
        )
      );
      for (const cause of d.causes) {
        const rowEl = el("div", "wf-row");
        const bar = el("div", "wf-bar");
        const fill = el("div", `wf-fill wf-${cause.factor}`);
        fill.style.width = `${Math.max(1, cause.share * 100).toFixed(1)}%`;
        bar.append(fill);
        rowEl.append(
          el("div", "wf-label", cause.factor.replace(/_/g, " ")),
          bar,
          el("div", "wf-value", `${ms(cause.ms)} \xB7 ${percent(cause.share)}`),
          el("div", `tag tag-${cause.confidence}`, cause.confidence)
        );
        box.append(rowEl, el("div", "wf-detail", cause.detail));
      }
      for (const note of d.notes ?? []) box.append(el("p", "detail-note", note));
    }
    const context = el("div", "detail-context");
    context.append(
      stat("arrived", clock(r.arrivedAt)),
      stat("queued on arrival", gaugeOrAbsent(r.queueDepthAtArrival)),
      stat("running on arrival", gaugeOrAbsent(r.runningAtArrival)),
      stat(
        "KV usage on arrival",
        r.tokenUsageAtArrival < 0 ? "metrics unavailable" : percent(r.tokenUsageAtArrival, 1)
      ),
      stat(
        "prefix tree expected",
        r.cacheMatch ? `${count(r.cacheMatch.expectedCachedTokens)} tok cached` : ABSENT
      ),
      stat(
        "shortfall against the server",
        r.cacheMatch && r.cacheMatch.shortfall > 0 ? `${count(r.cacheMatch.shortfall)} tok \u2014 something was dropped` : "none"
      )
    );
    box.append(context);
    if (r.error) box.append(el("p", "detail-error", r.error));
    if (r.promptPreview) {
      box.append(el("div", "prompt-label", r.promptTruncated ? "prompt (head; the body outgrew the inspection cap)" : "prompt"));
      box.append(el("pre", "prompt", r.promptPreview));
    }
    return box;
  }
};
function columns() {
  return [
    {
      key: "when",
      label: "arrived",
      sortable: true,
      value: (r) => r.arrivedAt,
      render: (r) => clock(r.arrivedAt)
    },
    { key: "path", label: "endpoint", sortable: true, value: (r) => r.path },
    { key: "model", label: "model", sortable: true, value: (r) => r.model || ABSENT },
    {
      key: "status",
      label: "status",
      sortable: true,
      value: (r) => r.status,
      render: (r) => {
        const tag = el("span", `status status-${r.status}`, r.status.replace("_", " "));
        return tag;
      }
    },
    {
      key: "total",
      label: "total",
      sortable: true,
      align: "end",
      value: (r) => span(r.finishedAt, r.arrivedAt),
      render: (r) => ms(span(r.finishedAt, r.arrivedAt)),
      text: (r) => ms(span(r.finishedAt, r.arrivedAt))
    },
    {
      key: "ttft",
      label: "TTFT",
      sortable: true,
      align: "end",
      value: (r) => r.stream ? span(r.firstTokenAt, r.arrivedAt) : null,
      render: (r) => r.stream ? ms(span(r.firstTokenAt, r.arrivedAt)) : ABSENT
    },
    {
      key: "prompt",
      label: "prompt",
      sortable: true,
      align: "end",
      value: (r) => r.promptTokens,
      render: (r) => count(r.promptTokens)
    },
    {
      key: "cached",
      label: "cached",
      sortable: true,
      align: "end",
      value: (r) => r.cachedTokens < 0 ? null : r.cachedTokens,
      render: (r) => r.cachedTokens < 0 ? ABSENT : `${count(r.cachedTokens)} \xB7 ${cachedShare(r)}`
    },
    {
      key: "out",
      label: "output",
      sortable: true,
      align: "end",
      value: (r) => r.completionTokens,
      render: (r) => count(r.completionTokens)
    },
    {
      key: "why",
      label: "largest slice",
      sortable: true,
      value: (r) => r.diagnosis?.headline ?? null,
      render: (r) => (r.diagnosis?.headline ?? ABSENT).replace(/_/g, " ")
    }
  ];
}
function span(end, start) {
  return end && end > 0 ? end - start : -1;
}
function cachedShare(r) {
  if (r.cachedTokens < 0 || r.promptTokens <= 0) return ABSENT;
  return percent(r.cachedTokens / r.promptTokens);
}
function cacheBucket(r) {
  if (r.cachedTokens < 0 || r.promptTokens <= 0) return "unreported";
  const share = r.cachedTokens / r.promptTokens;
  if (share >= 0.7) return "mostly cached";
  if (share > 0) return "partly cached";
  return "not cached";
}
function decodeRate(r) {
  const decode = span(r.finishedAt, r.firstTokenAt ?? 0);
  if (!r.firstTokenAt || decode <= 0 || r.completionTokens <= 0) return ABSENT;
  return `${Math.round(r.completionTokens / (decode / 1e3))} tok/s`;
}
function gaugeOrAbsent(value) {
  return value < 0 ? "metrics unavailable" : count(value);
}
function stat(label, value) {
  const box = el("div", "detail-stat");
  box.append(el("div", "detail-stat-label", label), el("div", "detail-stat-value", value));
  return box;
}
var DETAIL_STYLES = `
.detail { display: grid; gap: 12px; padding: 12px 4px 16px; }
.detail-head, .detail-context { display: flex; flex-wrap: wrap; gap: 18px; }
.detail-stat-label { font-size: 11px; color: var(--muted); text-transform: uppercase; letter-spacing: .05em; }
.detail-stat-value { font-size: 15px; font-variant-numeric: tabular-nums; }
.detail-note { margin: 0; color: var(--muted); }
.detail-error { margin: 0; color: var(--failure); }
.wf-row { display: grid; grid-template-columns: 170px 1fr 150px 72px; gap: 10px; align-items: center; }
.wf-bar { background: var(--panel-2); border-radius: 3px; height: 10px; overflow: hidden; }
.wf-fill { height: 100%; background: var(--accent); }
.wf-fill.wf-queue_wait { background: var(--warn); }
.wf-fill.wf-prefill_uncached_tokens { background: var(--failure); }
.wf-fill.wf-decode_contention { background: var(--warn); }
.wf-fill.wf-unattributed { background: var(--muted); }
.wf-value { text-align: right; font-variant-numeric: tabular-nums; }
.wf-detail { grid-column: 1 / -1; color: var(--muted); font-size: 12px; margin: -2px 0 8px 180px; }
.tag { font-size: 10px; text-transform: uppercase; letter-spacing: .06em; padding: 2px 6px; border-radius: 999px; border: 1px solid var(--border); color: var(--muted); }
.tag-measured { color: var(--success); border-color: var(--success); }
.tag-inferred { color: var(--warn); border-color: var(--warn); }
.status { padding: 2px 8px; border-radius: 999px; border: 1px solid var(--border); font-size: 11px; }
.status-failed { color: var(--failure); border-color: var(--failure); }
.status-in_flight { color: var(--running); border-color: var(--running); }
.prompt { white-space: pre-wrap; background: var(--panel-2); padding: 10px; border-radius: 6px; max-height: 220px; overflow: auto; margin: 0; }
.prompt-label { font-size: 11px; color: var(--muted); text-transform: uppercase; letter-spacing: .05em; }
`;

// src/timeline.ts
import "https://sites.pazer.build/js-snippets/branch/library/ui/timeline-view.js";
var INITIAL_WINDOW_MS = 9e4;
var MARKER_KINDS = /* @__PURE__ */ new Set([
  "cache.evicted",
  "cache.flushed",
  "memory.pressure",
  "scheduler.stalled",
  "scheduler.preempted",
  "upstream.unreachable"
]);
var Timeline = class {
  view;
  markers = [];
  onSelect = null;
  framed = false;
  constructor(root) {
    this.view = document.createElement("timeline-view");
    this.view.className = "timeline";
    this.view.legendEntries = [
      { glyph: "hatch", text: "waiting: queued and prefilling, before the first token" },
      { glyph: "solid", text: "decoding: first token to last" },
      { glyph: "emphasis", text: "over the slow threshold, or failed" },
      { glyph: "dim", text: "no cached prefix \u2014 the whole prompt was prefilled" }
    ];
    this.view.tooltipFor = (hit) => this.tooltip(hit);
    this.view.addEventListener("intervalclick", (ev) => {
      const detail = ev.detail;
      const id = detail?.interval?.id;
      if (id && this.onSelect) this.onSelect(id);
    });
    root.append(this.view);
  }
  /** Registers the callback fired when a bar is clicked. */
  select(handler) {
    this.onSelect = handler;
  }
  /** Recomputes the markers from the event log. */
  setEvents(events) {
    this.markers = events.filter((e) => MARKER_KINDS.has(e.kind)).slice(-60).map((e) => ({
      time: e.time,
      label: markerLabel(e),
      kind: e.kind === "cache.evicted" || e.kind === "cache.flushed" ? "emphasis" : ""
    }));
  }
  setRequests(records) {
    const lanes = /* @__PURE__ */ new Map();
    const intervals = [];
    const now = Date.now();
    let earliest = now;
    for (const r of records) {
      const laneId = r.path || "unknown";
      if (!lanes.has(laneId)) {
        lanes.set(laneId, { id: laneId, label: laneId, group: r.model || "model" });
      }
      earliest = Math.min(earliest, r.arrivedAt);
      const firstToken = r.firstTokenAt && r.firstTokenAt > 0 ? r.firstTokenAt : null;
      const end = r.finishedAt && r.finishedAt > 0 ? r.finishedAt : null;
      const segments = [];
      if (firstToken) {
        segments.push({ start: r.arrivedAt, end: firstToken, kind: "waiting" });
        segments.push({ start: firstToken, end, kind: "" });
      } else {
        segments.push({ start: r.arrivedAt, end, kind: "waiting" });
      }
      intervals.push({
        id: r.id,
        laneId,
        start: r.arrivedAt,
        end,
        label: barLabel(r),
        labelTiers: [barLabel(r), `${count(r.promptTokens)}p`, ""],
        category: cacheCategory(r),
        state: barState(r),
        segments,
        data: r
      });
    }
    this.view.setData({
      lanes: [...lanes.values()],
      intervals,
      markers: this.markers,
      coverage: { start: earliest, end: now }
    });
    if (!this.framed && intervals.length > 0) {
      this.framed = true;
      this.view.setViewport(now - INITIAL_WINDOW_MS, now);
    }
  }
  tooltip(hit) {
    const r = hit.interval?.data;
    if (!r) return null;
    const box = el("div", "tooltip");
    box.append(el("div", "tooltip-title", `${r.path} \xB7 ${r.id}`));
    const add = (label, value) => {
      const row = el("div", "tooltip-row");
      row.append(el("span", "tooltip-label", label), el("span", "tooltip-value", value));
      box.append(row);
    };
    add("arrived", clock(r.arrivedAt));
    add("time to first token", r.stream ? ms(ttft(r)) : "not streamed, so unobservable");
    add("total", ms(total(r)));
    add("prompt", `${count(r.promptTokens)} tokens`);
    add(
      "cached prefix",
      r.cachedTokens < 0 ? "the server reported none" : `${count(r.cachedTokens)} tokens (${percent(r.promptTokens ? r.cachedTokens / r.promptTokens : 0)})`
    );
    add("output", `${count(r.completionTokens)} tokens`);
    if (r.diagnosis?.causes?.length) {
      const c = r.diagnosis.causes[0];
      add("largest slice", `${c.factor} \xB7 ${ms(c.ms)} \xB7 ${percent(c.share)}`);
    }
    if (r.error) add("error", r.error);
    box.append(el("div", "tooltip-foot", "click the bar for the full attribution"));
    return box;
  }
};
function ttft(r) {
  return r.firstTokenAt && r.firstTokenAt > 0 ? r.firstTokenAt - r.arrivedAt : -1;
}
function total(r) {
  return r.finishedAt && r.finishedAt > 0 ? r.finishedAt - r.arrivedAt : -1;
}
function barLabel(r) {
  const cached = r.cachedTokens > 0 && r.promptTokens > 0 ? ` \xB7 ${percent(r.cachedTokens / r.promptTokens)} cached` : "";
  const t = total(r);
  return `${t < 0 ? "running" : ms(t)}${cached}`;
}
function cacheCategory(r) {
  if (r.cachedTokens < 0) return "cache-unknown";
  if (r.promptTokens <= 0) return "cache-unknown";
  const share = r.cachedTokens / r.promptTokens;
  if (share >= 0.8) return "cache-hot";
  if (share >= 0.3) return "cache-warm";
  if (share > 0) return "cache-cool";
  return "cache-cold";
}
function barState(r) {
  if (r.status === "failed") return "failed";
  if (r.status === "aborted") return "cancelled";
  if (r.diagnosis?.slow) return "emphasis";
  if (r.cachedTokens === 0) return "dim";
  return "";
}
function markerLabel(e) {
  const tokens = e.fields?.tokens;
  if (e.kind === "cache.evicted") {
    return typeof tokens === "number" ? `\u2212${count(tokens)} tok` : "evicted";
  }
  return e.kind.replace(/^[^.]+\./, "").replace(/_/g, " ");
}

// src/main.ts
var PULL_INTERVAL_MS = 1500;
function mount(id) {
  const node = document.getElementById(id);
  if (!node) throw new Error(`the page is missing #${id}, so the UI cannot be built`);
  return node;
}
function main() {
  const header = new Header(mount("header"));
  const gauges = new Gauges(mount("gauges"));
  const timeline = new Timeline(mount("timeline"));
  const cachePanel = new CachePanel(mount("cache"));
  const table = new RequestsTable(mount("requests"));
  const feed = new Feed(mount("feed"));
  const errorBar = mount("error-bar");
  timeline.select((id) => table.reveal(id));
  let latestStatus = null;
  const showError = (message) => {
    errorBar.textContent = message;
    errorBar.classList.remove("hidden");
  };
  const clearError = () => {
    errorBar.textContent = "";
    errorBar.classList.add("hidden");
  };
  openFeed({
    onStatus(status) {
      latestStatus = status;
      header.update(status);
      gauges.update(status);
    },
    onEvent(event) {
      feed.add(event);
      timeline.setEvents(feed.all());
    },
    onConnectionChange(connected, detail) {
      header.setConnection(connected, detail);
      if (!connected) showError(`The live feed dropped and is ${detail}. Nothing on this page is current.`);
      else clearError();
    }
  });
  const pull = async () => {
    try {
      const [{ requests }, cache] = await Promise.all([api.requests(300), api.cache(100)]);
      table.setRequests(requests);
      timeline.setRequests(requests);
      if (latestStatus) cachePanel.update(cache, latestStatus);
      clearError();
    } catch (err) {
      showError(`The dashboard could not refresh its own data: ${String(err)}`);
    }
  };
  void (async () => {
    try {
      const [status, { events }] = await Promise.all([api.status(), api.events(1e3)]);
      latestStatus = status;
      header.update(status);
      gauges.update(status);
      feed.replace(events);
      timeline.setEvents(events);
    } catch (err) {
      showError(`The dashboard could not load its initial state: ${String(err)}`);
    }
    await pull();
    window.setInterval(() => void pull(), PULL_INTERVAL_MS);
  })();
}
try {
  main();
} catch (err) {
  const box = el("div", "fatal");
  box.textContent = `sglang-dash could not start its UI: ${String(err)}`;
  document.body.prepend(box);
  throw err;
}
