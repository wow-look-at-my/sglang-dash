// The activity feed: every event the collectors published, filterable by
// severity and by family.

import "https://sites.pazer.build/js-snippets/branch/library/ui/activity-feed.js";
import type {
	ActivityEntry,
	ActivityFeedElement,
} from "https://sites.pazer.build/js-snippets/branch/library/ui/activity-feed.js";

import { el } from "./fmt.js";
import type { DashEvent } from "./types.js";

export class Feed {
	private readonly view: ActivityFeedElement;
	private events: DashEvent[] = [];
	private readonly cap: number;

	constructor(root: HTMLElement, cap = 2000) {
		this.cap = cap;
		this.view = document.createElement("activity-feed") as ActivityFeedElement;
		this.view.className = "feed";
		this.view.setAttribute("family-chips", "");
		this.view.setAttribute("storage-key", "sglang-dash.feed");
		this.view.setAttribute("placeholder", "filter events…");
		this.view.emptyText = "Nothing has happened yet.";
		this.view.messageRenderer = (message, entry) => renderMessage(message, entry);
		root.append(this.view);
	}

	replace(events: DashEvent[]): void {
		this.events = events.slice(-this.cap);
		this.render();
	}

	add(event: DashEvent): void {
		this.events.push(event);
		if (this.events.length > this.cap) this.events = this.events.slice(-this.cap);
		this.render();
	}

	all(): DashEvent[] {
		return this.events;
	}

	private render(): void {
		this.view.entries = [...this.events]
			.reverse()
			.map((e) => ({ time: e.time, kind: e.kind, message: e.message, ...(e.fields ?? {}) }));
	}
}

/** An eviction's evidence is the part worth reading, so it is rendered as
 * its own list under the message rather than folded into a single long line. */
function renderMessage(message: string, entry: ActivityEntry): Node | string {
	const evidence = entry.evidence;
	if (!Array.isArray(evidence) || evidence.length === 0) return message;
	const box = el("div", "feed-message");
	box.append(el("div", "", message));
	const list = el("ul", "evidence");
	for (const line of evidence) list.append(el("li", "", String(line)));
	box.append(list);
	return box;
}
