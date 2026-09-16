// The request table, and the attribution that opens under a row.
//
// The expanded row answers "why did this request take that long". It is a
// waterfall of the factors, each with the milliseconds it took, its share of
// the wall time, and whether the number was measured or apportioned.

import "https://sites.pazer.build/js-snippets/branch/library/ui/data-table.js";
import type {
	DataTableElement,
	TableColumn,
} from "https://sites.pazer.build/js-snippets/branch/library/ui/data-table.js";

import { ABSENT, clock, count, el, ms, percent } from "./fmt.js";
import type { RequestRecord } from "./types.js";

type Row = RequestRecord;

export class RequestsTable {
	private readonly table: DataTableElement;
	private records: RequestRecord[] = [];

	constructor(root: HTMLElement) {
		this.table = document.createElement("data-table") as DataTableElement;
		this.table.className = "requests-table";
		this.table.setAttribute("searchable", "");
		this.table.setAttribute("placeholder", "filter by id, model, prompt text, error…");
		this.table.setAttribute("storage-key", "sglang-dash.requests");
		this.table.emptyText = "No request has been proxied yet.";
		this.table.rowId = (r: Row) => r.id;
		this.table.rowClass = (r: Row) =>
			r.status === "failed" ? "row-failed" : r.diagnosis?.slow ? "row-slow" : "";
		this.table.searchText = (r: Row) =>
			[r.id, r.model, r.path, r.status, r.error ?? "", r.promptPreview ?? ""].join(" ");
		this.table.columns = columns();
		this.table.facets = [
			{
				key: "status",
				label: "status",
				of: (r: Row) => r.status,
				order: ["in_flight", "done", "failed", "aborted"],
			},
			{
				key: "speed",
				label: "speed",
				of: (r: Row) => (r.diagnosis?.slow ? "slow" : r.finishedAt ? "normal" : "running"),
				order: ["slow", "normal", "running"],
			},
			{
				key: "cache",
				label: "prefix cache",
				of: (r: Row) => cacheBucket(r),
				order: ["mostly cached", "partly cached", "not cached", "unreported"],
			},
			{ key: "model", label: "model", of: (r: Row) => r.model || "unnamed" },
		];
		this.table.detailFor = (r: Row) => this.detail(r);
		// The table styles its own cells, so the classes the renderers emit have
		// to be declared inside its shadow root.
		this.table.styleText = DETAIL_STYLES;
		root.append(this.table);
	}

	setRequests(records: RequestRecord[]): void {
		this.records = records;
		this.table.rows = records;
	}

	/** Opens a single request's detail, used when a timeline bar is clicked. */
	reveal(id: string): void {
		const row = this.records.find((r) => r.id === id);
		if (!row) return;
		(this.table as unknown as { expanded: string[] }).expanded = [id];
		this.table.scrollIntoView({ behavior: "smooth", block: "center" });
	}

	private detail(r: Row): Node {
		const box = el("div", "detail");

		const total = span(r.finishedAt, r.arrivedAt);
		const head = el("div", "detail-head");
		head.append(
			stat("total", ms(total)),
			stat("to first token", r.stream ? ms(span(r.firstTokenAt, r.arrivedAt)) : "not streamed"),
			stat("prompt", `${count(r.promptTokens)} tok`),
			stat(
				"from cache",
				r.cachedTokens < 0 ? "unreported" : `${count(r.cachedTokens)} tok · ${cachedShare(r)}`,
			),
			stat("output", `${count(r.completionTokens)} tok`),
			stat("decode rate", decodeRate(r)),
		);
		box.append(head);

		const d = r.diagnosis;
		if (!d) {
			box.append(
				el(
					"p",
					"detail-note",
					r.status === "in_flight"
						? "This request is still running, so there is nothing to attribute yet."
						: "This request finished without a diagnosis.",
				),
			);
		} else {
			box.append(
				el(
					"p",
					"detail-note",
					d.slow
						? `Flagged slow: ${ms(total)} against a ${ms(d.thresholdMs)} threshold.`
						: `Within normal range: ${ms(total)} against a ${ms(d.thresholdMs)} threshold.`,
				),
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
					el("div", "wf-value", `${ms(cause.ms)} · ${percent(cause.share)}`),
					el("div", `tag tag-${cause.confidence}`, cause.confidence),
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
				r.tokenUsageAtArrival < 0 ? "metrics unavailable" : percent(r.tokenUsageAtArrival, 1),
			),
			stat(
				"prefix tree expected",
				r.cacheMatch
					? `${count(r.cacheMatch.expectedCachedTokens)} tok cached`
					: ABSENT,
			),
			stat(
				"shortfall against the server",
				r.cacheMatch && r.cacheMatch.shortfall > 0
					? `${count(r.cacheMatch.shortfall)} tok — something was dropped`
					: "none",
			),
		);
		box.append(context);

		if (r.error) box.append(el("p", "detail-error", r.error));
		if (r.promptPreview) {
			box.append(el("div", "prompt-label", r.promptTruncated ? "prompt (head; the body outgrew the inspection cap)" : "prompt"));
			box.append(el("pre", "prompt", r.promptPreview));
		}
		return box;
	}
}

function columns(): TableColumn[] {
	return [
		{
			key: "when", label: "arrived", sortable: true,
			value: (r: Row) => r.arrivedAt,
			render: (r: Row) => clock(r.arrivedAt),
		},
		{ key: "path", label: "endpoint", sortable: true, value: (r: Row) => r.path },
		{ key: "model", label: "model", sortable: true, value: (r: Row) => r.model || ABSENT },
		{
			key: "status", label: "status", sortable: true,
			value: (r: Row) => r.status,
			render: (r: Row) => {
				const tag = el("span", `status status-${r.status}`, r.status.replace("_", " "));
				return tag;
			},
		},
		{
			key: "total", label: "total", sortable: true, align: "end",
			value: (r: Row) => span(r.finishedAt, r.arrivedAt),
			render: (r: Row) => ms(span(r.finishedAt, r.arrivedAt)),
			text: (r: Row) => ms(span(r.finishedAt, r.arrivedAt)),
		},
		{
			key: "ttft", label: "TTFT", sortable: true, align: "end",
			value: (r: Row) => (r.stream ? span(r.firstTokenAt, r.arrivedAt) : null),
			render: (r: Row) => (r.stream ? ms(span(r.firstTokenAt, r.arrivedAt)) : ABSENT),
		},
		{
			key: "prompt", label: "prompt", sortable: true, align: "end",
			value: (r: Row) => r.promptTokens,
			render: (r: Row) => count(r.promptTokens),
		},
		{
			key: "cached", label: "cached", sortable: true, align: "end",
			value: (r: Row) => (r.cachedTokens < 0 ? null : r.cachedTokens),
			render: (r: Row) => (r.cachedTokens < 0 ? ABSENT : `${count(r.cachedTokens)} · ${cachedShare(r)}`),
		},
		{
			key: "out", label: "output", sortable: true, align: "end",
			value: (r: Row) => r.completionTokens,
			render: (r: Row) => count(r.completionTokens),
		},
		{
			key: "why", label: "largest slice", sortable: true,
			value: (r: Row) => r.diagnosis?.headline ?? null,
			render: (r: Row) => (r.diagnosis?.headline ?? ABSENT).replace(/_/g, " "),
		},
	];
}

function span(end: number | undefined, start: number): number {
	return end && end > 0 ? end - start : -1;
}

function cachedShare(r: Row): string {
	if (r.cachedTokens < 0 || r.promptTokens <= 0) return ABSENT;
	return percent(r.cachedTokens / r.promptTokens);
}

function cacheBucket(r: Row): string {
	if (r.cachedTokens < 0 || r.promptTokens <= 0) return "unreported";
	const share = r.cachedTokens / r.promptTokens;
	if (share >= 0.7) return "mostly cached";
	if (share > 0) return "partly cached";
	return "not cached";
}

function decodeRate(r: Row): string {
	const decode = span(r.finishedAt, r.firstTokenAt ?? 0);
	if (!r.firstTokenAt || decode <= 0 || r.completionTokens <= 0) return ABSENT;
	return `${Math.round(r.completionTokens / (decode / 1000))} tok/s`;
}

function gaugeOrAbsent(value: number): string {
	return value < 0 ? "metrics unavailable" : count(value);
}

function stat(label: string, value: string): HTMLElement {
	const box = el("div", "detail-stat");
	box.append(el("div", "detail-stat-label", label), el("div", "detail-stat-value", value));
	return box;
}

const DETAIL_STYLES = `
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
