// Wires the panels to the live feed.
//
// Update paths run side by side. Status frames and events arrive pushed, over
// the server-sent stream, and drive the header, the gauges and the feed. The
// request list and the cache snapshot are pulled on a short interval, because
// both are whole collections and a delta protocol for them would buy nothing
// at this size.

import { api, openFeed } from "./api.js";
import { CachePanel } from "./cachepanel.js";
import { el } from "./fmt.js";
import { Feed } from "./feed.js";
import { Gauges } from "./gauges.js";
import { Header } from "./header.js";
import { RequestsTable } from "./requeststable.js";
import { Timeline } from "./timeline.js";
import type { Status } from "./types.js";

const PULL_INTERVAL_MS = 1500;

function mount(id: string): HTMLElement {
	const node = document.getElementById(id);
	if (!node) throw new Error(`the page is missing #${id}, so the UI cannot be built`);
	return node;
}

function main(): void {
	const header = new Header(mount("header"));
	const gauges = new Gauges(mount("gauges"));
	const timeline = new Timeline(mount("timeline"));
	const cachePanel = new CachePanel(mount("cache"));
	const table = new RequestsTable(mount("requests"));
	const feed = new Feed(mount("feed"));
	const errorBar = mount("error-bar");

	timeline.select((id) => table.reveal(id));

	let latestStatus: Status | null = null;

	const showError = (message: string): void => {
		errorBar.textContent = message;
		errorBar.classList.remove("hidden");
	};
	const clearError = (): void => {
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
		},
	});

	// The pull loop keeps its fixed cadence after a failure. A dashboard that
	// stops refreshing on a bad fetch is worse than a dashboard that says the
	// fetch failed.
	const pull = async (): Promise<void> => {
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

	void (async (): Promise<void> => {
		try {
			const [status, { events }] = await Promise.all([api.status(), api.events(1000)]);
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
	// A UI that fails to build must say so on the page. A blank screen reads as
	// an idle server.
	const box = el("div", "fatal");
	box.textContent = `sglang-dash could not start its UI: ${String(err)}`;
	document.body.prepend(box);
	throw err;
}
