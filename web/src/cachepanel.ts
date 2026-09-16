// The prefix-cache panel: what is cached, how full the pool is, how close each
// prefix is to being dropped, and what was lost when something was.
//
// SGLang's radix cache has no TTL. Nothing expires on a clock; the server drops
// the least recently used prefix when it needs the room. So the panel reports
// both numbers that actually decide a prefix's fate — its age since last use,
// and its rank in the least-recently-used order — instead of a countdown that
// would be fiction.

import "https://sites.pazer.build/js-snippets/branch/library/ui/dag-view.js";
import type {
	DagHit,
	DagNode,
	DagViewElement,
} from "https://sites.pazer.build/js-snippets/branch/library/ui/dag-view.js";

import { ABSENT, age, clock, confidenceBadge, count, el, field, ms, percent } from "./fmt.js";
import type { CacheNode, CacheSnapshot, Eviction, Status } from "./types.js";

/** How many nodes the graph draws before it stops adding cold ones. */
const MAX_DRAWN_NODES = 300;

export class CachePanel {
	private readonly graph: DagViewElement;
	private readonly summary: HTMLElement;
	private readonly meter: HTMLElement;
	private readonly meterNote: HTMLElement;
	private readonly lruList: HTMLElement;
	private readonly evictionList: HTMLElement;
	private readonly truncation: HTMLElement;
	private snapshot: CacheSnapshot | null = null;

	constructor(root: HTMLElement) {
		const left = el("div", "cache-left");
		this.summary = el("div", "cache-summary");
		// The pool meter is a <scratch-progress>: it already carries the
		// design language's bar geometry and its accent/signal/danger states.
		this.meter = document.createElement("scratch-progress");
		this.meter.setAttribute("max", "100");
		this.meterNote = el("div", "meter-note", "");
		this.truncation = el("div", "notice", "");
		this.graph = document.createElement("dag-view") as DagViewElement;
		this.graph.className = "cache-graph";
		this.graph.setAttribute("orientation", "LR");
		this.graph.setAttribute("empty-text", "No prompt has been observed yet, so there is no prefix tree to draw.");
		this.graph.tooltipFor = (hit) => this.nodeTooltip(hit);
		left.append(this.summary, this.meter, this.meterNote, this.truncation, this.graph);

		const right = el("div", "cache-right");
		right.append(
			sectionTitle("closest to eviction", "coldest first — this is the order the server's LRU would drop them in"),
		);
		this.lruList = el("div", "lru-list");
		right.append(this.lruList);
		right.append(sectionTitle("what was lost, and why", "each row carries the evidence the reason was drawn from"));
		this.evictionList = el("div", "eviction-list");
		right.append(this.evictionList);

		root.append(left, right);
	}

	update(snapshot: CacheSnapshot, status: Status): void {
		this.snapshot = snapshot;
		this.renderSummary(snapshot, status);
		this.renderMeter(status);
		this.renderGraph(snapshot);
		this.renderLRU(snapshot);
		this.renderEvictions(snapshot.evictions);
	}

	private renderSummary(s: CacheSnapshot, status: Status): void {
		this.summary.replaceChildren(
			field("live prefix chunks", count(s.stats.liveNodes), "inferred"),
			field("tokens tracked", count(s.stats.trackedTokens), "inferred"),
			field("distinct prompt roots", count(s.stats.distinctRoots), "inferred"),
			field(
				"prompt tokens served from cache",
				s.stats.promptTokens > 0 ? `${count(s.stats.hitTokens)} of ${count(s.stats.promptTokens)}` : ABSENT,
				"measured",
			),
			field("token hit rate, as observed here", percent(s.stats.tokenHitRate, 1), "measured"),
			field(
				"server's own hit rate",
				status.metrics.ok && status.metrics.gauges.cache_hit_rate !== undefined
					? percent(status.metrics.gauges.cache_hit_rate, 1)
					: "not exported by this server",
				"measured",
			),
			field(
				"chars per token, calibrated",
				s.stats.calibrationSamples > 0
					? `${s.stats.charsPerToken.toFixed(2)} over ${count(s.stats.calibrationSamples)} requests`
					: "no request has reported a token count yet",
				"derived",
			),
			field("believed evictions", `${count(s.stats.evictions)} (~${count(s.stats.evictedTokens)} tokens)`, "inferred"),
		);
		if (s.redacted) {
			this.summary.append(
				field("prompt text", "withheld: the dashboard was started with -redact-prompts", "measured"),
			);
		}
	}

	private renderMeter(status: Status): void {
		const usage = status.metrics.ok ? status.metrics.gauges.token_usage : undefined;
		if (usage === undefined || !Number.isFinite(usage)) {
			this.meter.classList.add("meter-absent");
			this.meter.setAttribute("value", "0");
			this.meterNote.textContent = status.metrics.ok
				? "this server does not export token_usage, so how full the KV pool is cannot be read from here"
				: "the metrics scrape is failing, so pool occupancy is unknown";
			return;
		}
		this.meter.classList.remove("meter-absent");
		const pct = Math.max(0, Math.min(1, usage));
		this.meter.setAttribute("value", (pct * 100).toFixed(1));
		this.meter.setAttribute("state", pct >= 0.9 ? "danger" : pct >= 0.7 ? "accent" : "signal");
		this.meterNote.textContent =
			`KV pool ${percent(usage, 1)} full — ${count(status.metrics.gauges.used_tokens)} tokens held. ` +
			(pct >= 0.9
				? "At this level the server is evicting cached prefixes to admit new work."
				: "The cached prefixes below are safe while there is room.");
	}

	private renderGraph(s: CacheSnapshot): void {
		// The warmest nodes take priority, so the cap trims the cold tail
		// rather than whatever the map iterated over last.
		const ranked = [...s.nodes].sort((a, b) => b.lastAccess - a.lastAccess);
		const drawn = ranked.slice(0, MAX_DRAWN_NODES);
		const ids = new Set(drawn.map((n) => n.id));
		const rootOf = rootResolver(s.nodes);
		const nodes: DagNode[] = drawn.map((n) => ({
			id: n.id,
			label: n.preview ? shorten(n.preview, 46) : `chunk ${n.hash}`,
			sublabel: `${count(n.estTokens)} tok · ${count(n.hits)} hits · depth ${n.depth}`,
			category: rootOf(n),
			state: n.evicted ? "missing" : n.inFlight > 0 ? "blocked" : "done",
		}));
		const edges = drawn
			.filter((n) => n.parentId && ids.has(n.parentId))
			.map((n) => ({ from: n.parentId as string, to: n.id }));
		this.graph.setData({ nodes, edges });

		this.truncation.textContent =
			s.nodes.length > drawn.length
				? `Drawing the ${drawn.length} most recently used chunks of ${s.nodes.length}. The rest are still tracked and still counted above.`
				: "";
		this.truncation.classList.toggle("hidden", s.nodes.length <= drawn.length);
	}

	private renderLRU(s: CacheSnapshot): void {
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
					`idle ${age(n.lastAccess)} · ${count(n.estTokens)} tokens · ${count(n.hits)} hits · ${
						n.inFlight > 0 ? "pinned by a live request" : "droppable"
					}`,
				),
			);
			row.append(body);
			return row;
		});
		this.lruList.replaceChildren(
			...(rows.length > 0
				? rows
				: [el("div", "empty", "Nothing is cached yet. The first prompt through the proxy starts the tree.")]),
		);
	}

	private renderEvictions(evictions: Eviction[]): void {
		if (evictions.length === 0) {
			this.evictionList.replaceChildren(
				el(
					"div",
					"empty",
					"No prefix loss has been observed. An eviction is noticed when a prompt whose prefix this tree still holds comes back reporting fewer cached tokens than it should.",
				),
			);
			return;
		}
		const rows = [...evictions].reverse().slice(0, 20).map((ev) => {
			const row = el("div", "eviction-row");
			const head = el("div", "eviction-head");
			head.append(
				el("span", `reason reason-${ev.reason}`, ev.reason.replace(/_/g, " ")),
				confidenceBadge(ev.confidence),
				el("span", "eviction-when", clock(ev.time)),
			);
			row.append(head);
			row.append(
				el("div", "eviction-body", `~${count(ev.tokens)} tokens across ${count(ev.nodeIds.length)} chunks`),
			);
			if (ev.preview) row.append(el("div", "eviction-preview", shorten(ev.preview, 110)));
			const evidence = el("ul", "evidence");
			for (const line of ev.evidence) evidence.append(el("li", "", line));
			row.append(evidence);
			return row;
		});
		this.evictionList.replaceChildren(...rows);
	}

	private nodeTooltip(hit: DagHit): { title?: string; rows?: [string, string][] } | null {
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
				["state", n.evicted ? "believed dropped by the server" : "believed cached"],
			],
		};
	}
}

/** Every chunk takes the colour of the prompt root it hangs off, so a single
 * system prompt's whole subtree reads as a single family. The walk is
 * memoised because a deep tree would otherwise re-walk the same spine a
 * single time per node. */
function rootResolver(nodes: CacheNode[]): (node: CacheNode) => string {
	const byID = new Map(nodes.map((n) => [n.id, n]));
	const memo = new Map<string, string>();
	return (node: CacheNode): string => {
		const seen: string[] = [];
		let current: CacheNode | undefined = node;
		let root = node.hash;
		while (current) {
			const hit = memo.get(current.id);
			if (hit !== undefined) {
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

function shorten(text: string, max: number): string {
	return text.length <= max ? text : `${text.slice(0, max - 1)}…`;
}

function sectionTitle(title: string, note: string): HTMLElement {
	const head = el("div", "section-title");
	head.append(el("h3", "", title), el("p", "section-note", note));
	return head;
}
