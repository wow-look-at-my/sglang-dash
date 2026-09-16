//
// A gauge whose series the upstream does not export is not drawn flat at
// empty: it says so in place of the trace, because a flat green line and a
// metric that does not exist look identical and mean opposite things.

import "https://sites.pazer.build/js-snippets/branch/library/ui/perf-graph.js";
import type { PerfGraphElement } from "https://sites.pazer.build/js-snippets/branch/library/ui/perf-graph.js";

import { el } from "./fmt.js";
import type { Status } from "./types.js";

interface GaugeSpec {
	/** The gauge field in the status payload, or a quantile lookup. */
	key: string;
	label: string;
	unit: string;
	/** Reads the value out of a status frame, or null when it is unavailable. */
	read(status: Status): number | null;
	min?: number;
	max?: number;
	budget?: number;
	/** What this number means, shown under the gauge. */
	hint: string;
}

const SPECS: GaugeSpec[] = [
	{
		key: "running_reqs", label: "running", unit: "", min: 0,
		read: (s) => gauge(s, "running_reqs"),
		hint: "requests the server is decoding right now",
	},
	{
		key: "queued_reqs", label: "queued", unit: "", min: 0,
		read: (s) => gauge(s, "queued_reqs"),
		hint: "requests admitted but not yet scheduled — the first place latency hides",
	},
	{
		key: "token_usage", label: "KV usage", unit: "%", min: 0, max: 100, budget: 90,
		read: (s) => {
			const v = gauge(s, "token_usage");
			return v === null ? null : v * 100;
		},
		hint: "how full the KV pool is; past the dashed line the server starts evicting cached prefixes",
	},
	{
		key: "gen_throughput", label: "output", unit: "tok/s", min: 0,
		read: (s) => gauge(s, "gen_throughput"),
		hint: "generation throughput across the whole batch",
	},
	{
		key: "cache_hit_rate", label: "cache hit", unit: "%", min: 0, max: 100,
		read: (s) => {
			const v = gauge(s, "cache_hit_rate");
			return v === null ? null : v * 100;
		},
		hint: "the server's own prefix-cache hit rate, not the dashboard's reconstruction",
	},
	{
		key: "ttft_p50", label: "TTFT p50", unit: "ms", min: 0,
		read: (s) => quantile(s, "ttft", "p50"),
		hint: "time to first token at the median, off the server's histogram",
	},
	{
		key: "ttft_p99", label: "TTFT p99", unit: "ms", min: 0,
		read: (s) => quantile(s, "ttft", "p99"),
		hint: "the tail that a user actually complains about",
	},
	{
		key: "e2e_p99", label: "e2e p99", unit: "ms", min: 0,
		read: (s) => quantile(s, "e2e_latency", "p99"),
		hint: "whole-request latency at the tail",
	},
];

function gauge(status: Status, field: string): number | null {
	if (!status.metrics.ok) return null;
	const v = status.metrics.gauges[field];
	return v === undefined || !Number.isFinite(v) ? null : v;
}

function quantile(status: Status, family: string, q: "p50" | "p90" | "p99"): number | null {
	if (!status.metrics.ok) return null;
	const row = status.metrics.quantiles[family];
	if (!row || !Number.isFinite(row[q])) return null;
	return row[q] * 1000;
}

interface Mounted {
	spec: GaugeSpec;
	graph: PerfGraphElement;
	card: HTMLElement;
	missing: HTMLElement;
}

export class Gauges {
	private readonly mounted: Mounted[] = [];

	constructor(root: HTMLElement) {
		for (const spec of SPECS) {
			const card = el("div", "gauge-card");
			const graph = document.createElement("perf-graph") as PerfGraphElement;
			graph.setAttribute("label", spec.label);
			graph.setAttribute("unit", spec.unit);
			graph.setAttribute("history", "240");
			graph.setAttribute("height", "64");
			if (spec.min !== undefined) graph.setAttribute("min", String(spec.min));
			if (spec.max !== undefined) graph.setAttribute("max", String(spec.max));
			if (spec.budget !== undefined) graph.setAttribute("budget", String(spec.budget));

			const missing = el("div", "gauge-missing", "");
			card.append(graph, missing, el("div", "gauge-hint", spec.hint));
			root.append(card);
			this.mounted.push({ spec, graph, card, missing });
		}
	}

	update(status: Status): void {
		for (const m of this.mounted) {
			const value = m.spec.read(status);
			if (value === null) {
				m.card.classList.add("gauge-absent");
				m.missing.textContent = status.metrics.ok
					? `${m.spec.key} is not exported by this server`
					: "the metrics scrape is failing, so this gauge has nothing to draw";
				continue;
			}
			m.card.classList.remove("gauge-absent");
			m.missing.textContent = "";
			m.graph.push(value);
		}
	}
}
