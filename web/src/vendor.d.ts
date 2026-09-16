// Ambient shims for the js-snippets components, which are imported by URL at
// runtime. Each shim declares only the surface this dashboard actually uses.
//
// The library serves a real .d.ts next to every .js; fetching those into
// committed, freshness-gated files is the intended long-term mechanism, and
// these hand-written shims stand in until that generate step exists here.

// The Scratch Proto bundle registers every scratch-* element as an import side
// effect and exports nothing. The elements are driven by attributes alone.
declare module "https://sites.pazer.build/scratch_ui/branch/master/scratch-ui.js";

declare module "https://sites.pazer.build/js-snippets/branch/library/ui/perf-graph.js" {
	export class PerfGraphElement extends HTMLElement {
		push(value: number): void;
		clear(): void;
		refreshTheme(): void;
		label: string;
		unit: string;
		history: number;
		min: number | null;
		max: number | null;
		budget: number | null;
	}
}

declare module "https://sites.pazer.build/js-snippets/branch/library/ui/timeline-view.js" {
	export interface TimelineLane {
		id: string;
		label: string;
		group?: string;
	}
	export interface TimelineSegment {
		start: number;
		end?: number | null;
		kind?: string;
	}
	export interface TimelineInterval {
		id: string;
		laneId: string;
		start: number;
		end?: number | null;
		label?: string;
		labelTiers?: string[];
		category?: string;
		state?: string;
		segments?: TimelineSegment[];
		data?: unknown;
	}
	export interface TimelineMarker {
		time: number;
		label?: string;
		kind?: string;
	}
	export interface TimelineHit {
		type: string;
		interval?: TimelineInterval;
		lane?: TimelineLane;
	}
	export class TimelineViewElement extends HTMLElement {
		setData(data: {
			lanes?: TimelineLane[];
			intervals?: TimelineInterval[];
			markers?: TimelineMarker[];
			coverage?: { start: number; end: number };
		}): void;
		clear(): void;
		markFresh(ts?: number): void;
		setViewport(start: number, end: number): void;
		followNow: boolean;
		tooltipFor: ((hit: TimelineHit) => string | Node | null) | null;
		legendEntries: { glyph: string; text: string }[];
		staleAfterMs: number;
	}
}

declare module "https://sites.pazer.build/js-snippets/branch/library/ui/data-table.js" {
	export interface TableColumn {
		key: string;
		label: string;
		value?: (row: never) => unknown;
		render?: (row: never) => Node | string | null;
		text?: (row: never) => string;
		sortable?: boolean;
		align?: "start" | "end";
		className?: string;
	}
	export interface FacetGroup {
		key: string;
		label?: string;
		of: (row: never) => string | string[] | null;
		order?: string[];
		chipClass?: (bucket: string) => string;
	}
	export class DataTableElement extends HTMLElement {
		columns: TableColumn[];
		rows: unknown[];
		facets: FacetGroup[];
		rowId: ((row: never) => string) | null;
		rowClass: ((row: never) => string) | null;
		detailFor: ((row: never) => Node | null) | null;
		searchText: ((row: never) => string) | null;
		styleText: string;
		emptyText: string;
		clearFilter(): void;
	}
}

declare module "https://sites.pazer.build/js-snippets/branch/library/ui/activity-feed.js" {
	export interface ActivityEntry {
		time: number;
		kind: string;
		message: string;
		[field: string]: unknown;
	}
	export class ActivityFeedElement extends HTMLElement {
		entries: ActivityEntry[];
		messageRenderer: ((message: string, entry: ActivityEntry) => Node | string | null) | null;
		emptyText: string;
		storageKey: string;
		familyChips: boolean;
	}
}

declare module "https://sites.pazer.build/js-snippets/branch/library/ui/dag-view.js" {
	export interface DagNode {
		id: string;
		label: string;
		sublabel?: string;
		category?: string;
		state?: string;
	}
	export interface DagEdge {
		from: string;
		to: string;
		kind?: string;
	}
	export interface DagHit {
		node?: DagNode;
		edge?: DagEdge;
	}
	export class DagViewElement extends HTMLElement {
		setData(data: { nodes?: DagNode[]; edges?: DagEdge[] }): void;
		orientation: "TB" | "LR";
		tooltipFor:
			| ((hit: DagHit) => { title?: string; rows?: [string, string][] } | null)
			| null;
		selected: string | null;
		fit(pad?: number): void;
		focusNode(id: string, zoom?: number): void;
	}
}
