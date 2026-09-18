// The request timeline: lanes by endpoint, bars by request, with the wait that
// ends at TTFT and the decode after it drawn as separate segments.
//
// The segment split is the point of the panel. A bar that is mostly hatched
// waited; a bar that is mostly solid generated. Eviction and pressure events
// land as markers on the same axis, so "the slow ones start here" is something
// you see rather than something you correlate by timestamp.

import "https://sites.pazer.build/js-snippets/branch/library/ui/timeline-view.js";
import type {
	TimelineHit,
	TimelineInterval,
	TimelineLane,
	TimelineMarker,
	TimelineViewElement,
} from "https://sites.pazer.build/js-snippets/branch/library/ui/timeline-view.js";

import { clock, count, el, ms, percent } from "./fmt.js";
import type { DashEvent, RequestRecord } from "./types.js";

/** The window the chart opens on, before any gesture. */
const INITIAL_WINDOW_MS = 90_000;

/** Event kinds worth a vertical line on the request axis. */
const MARKER_KINDS = new Set([
	"cache.evicted",
	"cache.flushed",
	"memory.pressure",
	"scheduler.stalled",
	"scheduler.preempted",
	"upstream.unreachable",
]);

export class Timeline {
	private readonly view: TimelineViewElement;
	private markers: TimelineMarker[] = [];
	private onSelect: ((id: string) => void) | null = null;
	private framed = false;

	constructor(root: HTMLElement) {
		this.view = document.createElement("timeline-view") as TimelineViewElement;
		this.view.className = "timeline";
		this.view.legendEntries = [
			{ glyph: "hatch", text: "waiting: queued and prefilling, before the first token" },
			{ glyph: "solid", text: "decoding: first token to last" },
			{ glyph: "emphasis", text: "over the slow threshold, or failed" },
			{ glyph: "dim", text: "no cached prefix — the whole prompt was prefilled" },
		];
		this.view.tooltipFor = (hit) => this.tooltip(hit);
		this.view.addEventListener("intervalclick", (ev) => {
			const detail = (ev as CustomEvent<{ interval?: TimelineInterval }>).detail;
			const id = detail?.interval?.id;
			if (id && this.onSelect) this.onSelect(id);
		});
		root.append(this.view);
	}

	/** Registers the callback fired when a bar is clicked. */
	select(handler: (id: string) => void): void {
		this.onSelect = handler;
	}

	/** Recomputes the markers from the event log. */
	setEvents(events: DashEvent[]): void {
		this.markers = events
			.filter((e) => MARKER_KINDS.has(e.kind))
			.slice(-60)
			.map((e) => ({
				time: e.time,
				label: markerLabel(e),
				kind: e.kind === "cache.evicted" || e.kind === "cache.flushed" ? "emphasis" : "",
			}));
	}

	setRequests(records: RequestRecord[]): void {
		const lanes = new Map<string, TimelineLane>();
		const intervals: TimelineInterval[] = [];
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
				data: r,
			});
		}

		this.view.setData({
			lanes: [...lanes.values()],
			intervals,
			markers: this.markers,
			coverage: { start: earliest, end: now },
		});

		// The component's default window is minutes wide, which squashes a
		// short request into a pip. An explicit window on the opening paint
		// gives the bars width. After that the view belongs to the reader.
		if (!this.framed && intervals.length > 0) {
			this.framed = true;
			this.view.setViewport(now - INITIAL_WINDOW_MS, now);
		}
	}

	private tooltip(hit: TimelineHit): Node | null {
		const r = hit.interval?.data as RequestRecord | undefined;
		if (!r) return null;
		const box = el("div", "tooltip");
		box.append(el("div", "tooltip-title", `${r.path} · ${r.id}`));
		const add = (label: string, value: string): void => {
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
			r.cachedTokens < 0
				? "the server reported none"
				: `${count(r.cachedTokens)} tokens (${percent(r.promptTokens ? r.cachedTokens / r.promptTokens : 0)})`,
		);
		add("output", `${count(r.completionTokens)} tokens`);
		if (r.diagnosis?.causes?.length) {
			const c = r.diagnosis.causes[0];
			add("largest slice", `${c.factor} · ${ms(c.ms)} · ${percent(c.share)}`);
		}
		if (r.error) add("error", r.error);
		box.append(el("div", "tooltip-foot", "click the bar for the full attribution"));
		return box;
	}
}

function ttft(r: RequestRecord): number {
	return r.firstTokenAt && r.firstTokenAt > 0 ? r.firstTokenAt - r.arrivedAt : -1;
}

function total(r: RequestRecord): number {
	return r.finishedAt && r.finishedAt > 0 ? r.finishedAt - r.arrivedAt : -1;
}

function barLabel(r: RequestRecord): string {
	const cached = r.cachedTokens > 0 && r.promptTokens > 0 ? ` · ${percent(r.cachedTokens / r.promptTokens)} cached` : "";
	const t = total(r);
	return `${t < 0 ? "running" : ms(t)}${cached}`;
}

/** A bar's colour is how much of its prompt the cache already held. */
function cacheCategory(r: RequestRecord): string {
	if (r.cachedTokens < 0) return "cache-unknown";
	if (r.promptTokens <= 0) return "cache-unknown";
	const share = r.cachedTokens / r.promptTokens;
	if (share >= 0.8) return "cache-hot";
	if (share >= 0.3) return "cache-warm";
	if (share > 0) return "cache-cool";
	return "cache-cold";
}

function barState(r: RequestRecord): string {
	if (r.status === "failed") return "failed";
	if (r.status === "aborted") return "cancelled";
	if (r.diagnosis?.slow) return "emphasis";
	if (r.cachedTokens === 0) return "dim";
	return "";
}

/** A marker label sits on a single line at the top of the plot, so several
 * close together overlap. These stay short; the reason and its evidence are
 * in the activity feed and the cache panel, where there is room for them. */
function markerLabel(e: DashEvent): string {
	const tokens = e.fields?.tokens;
	if (e.kind === "cache.evicted") {
		return typeof tokens === "number" ? `−${count(tokens)} tok` : "evicted";
	}
	return e.kind.replace(/^[^.]+\./, "").replace(/_/g, " ");
}
