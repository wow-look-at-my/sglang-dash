// The dashboard's own API client, plus the live feed.

import type { CacheSnapshot, DashEvent, RequestRecord, Status } from "./types.js";

async function getJSON<T>(path: string): Promise<T> {
	const resp = await fetch(path, { headers: { accept: "application/json" } });
	if (!resp.ok) {
		throw new Error(`${path} returned ${resp.status} ${resp.statusText}: ${await resp.text()}`);
	}
	return (await resp.json()) as T;
}

export const api = {
	status: () => getJSON<Status>("/api/status"),
	events: (limit = 500) => getJSON<{ events: DashEvent[] }>(`/api/events?limit=${limit}`),
	requests: (limit = 300) => getJSON<{ requests: RequestRecord[] }>(`/api/requests?limit=${limit}`),
	cache: (evictions = 100) => getJSON<CacheSnapshot>(`/api/cache?evictions=${evictions}`),
};

export interface FeedHandlers {
	onStatus(status: Status): void;
	onEvent(event: DashEvent): void;
	onConnectionChange(connected: boolean, detail: string): void;
}

/** Opens the server-sent feed and keeps it open. */
export function openFeed(handlers: FeedHandlers): () => void {
	let source: EventSource | null = null;
	let timer = 0;
	let closed = false;

	const connect = (): void => {
		if (closed) return;
		source = new EventSource("/api/stream");
		source.onopen = () => handlers.onConnectionChange(true, "live");
		source.onmessage = (ev) => {
			let payload: { type: string; status?: Status; event?: DashEvent };
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
			timer = window.setTimeout(connect, 1000);
		};
	};

	connect();
	return () => {
		closed = true;
		window.clearTimeout(timer);
		source?.close();
	};
}
