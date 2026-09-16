// The status strip: what is being observed, whether the observation is
// working, and the counters that frame everything below it.

import { ABSENT, count, el, ms, percent } from "./fmt.js";
import type { Status } from "./types.js";

interface Stat {
	node: HTMLElement;
	value: HTMLElement;
	note: HTMLElement;
}

export class Header {
	private readonly mode: HTMLElement;
	private readonly target: HTMLElement;
	private readonly link: HTMLElement;
	private readonly led: HTMLElement;
	private readonly linkText: HTMLElement;
	private readonly stats = new Map<string, Stat>();

	constructor(private readonly root: HTMLElement) {
		const left = el("div", "header-identity");
		// The mode chip and the connection dot are Scratch Proto components, so
		// they carry the design language's own chip and LED treatment.
		this.mode = document.createElement("scratch-badge");
		this.mode.textContent = "starting";
		this.target = el("span", "header-target", "");
		left.append(el("span", "product", "sglang-dash"), this.mode, this.target);

		this.link = el("span", "link-state link-unknown");
		this.led = document.createElement("scratch-led");
		this.linkText = el("span", "", "connecting");
		this.link.append(this.led, this.linkText);
		const right = el("div", "header-stats");
		for (const [key, label] of [
			["running", "running"],
			["queued", "queued"],
			["kv", "KV used"],
			["throughput", "output"],
			["hit", "cache hit"],
			["ttft", "TTFT p50/p99"],
			["inflight", "in flight"],
			["slow", "slow"],
		] as const) {
			const node = el("div", "stat");
			const value = el("div", "stat-value", ABSENT);
			const note = el("div", "stat-note", "");
			node.append(el("div", "stat-label", label), value, note);
			right.append(node);
			this.stats.set(key, { node, value, note });
		}

		this.root.append(left, this.link, right);
	}

	setConnection(connected: boolean, detail: string): void {
		this.linkText.textContent = detail;
		this.link.className = `link-state ${connected ? "link-live" : "link-down"}`;
		// In the LED's language a pulse means work in flight, so a live stream
		// pulses green and a dropped one sits red and still.
		if (connected) {
			this.led.removeAttribute("state");
			this.led.setAttribute("live", "");
		} else {
			this.led.setAttribute("state", "bad");
			this.led.removeAttribute("live");
		}
	}

	update(status: Status): void {
		this.mode.textContent = status.mode;
		// Amber reads as caution, which is what fabricated traffic is.
		this.mode.setAttribute("variant", status.mode === "demo" ? "accent" : "signal");
		this.target.textContent =
			status.mode === "demo"
				? "simulated traffic — nothing on this screen came from a model server"
				: `observing ${status.upstream ?? ABSENT}`;

		const g = status.metrics.gauges;
		const ok = status.metrics.ok;
		const scrapeNote = ok ? "" : `metrics scrape failing: ${status.metrics.error ?? "unknown"}`;

		this.set("running", ok ? count(g.running_reqs) : ABSENT, scrapeNote || "on the server now");
		this.set("queued", ok ? count(g.queued_reqs) : ABSENT, scrapeNote || "waiting to be scheduled");
		this.set("kv", ok ? percent(g.token_usage) : ABSENT, ok ? `${count(g.used_tokens)} tokens held` : scrapeNote);
		this.set("throughput", ok ? `${count(g.gen_throughput)} tok/s` : ABSENT, scrapeNote || "generation throughput");
		this.set(
			"hit",
			ok && g.cache_hit_rate !== undefined ? percent(g.cache_hit_rate, 1) : ABSENT,
			ok && g.cache_hit_rate === undefined ? "this server does not report it" : "as the server reports it",
		);

		const ttft = status.metrics.quantiles.ttft;
		this.set(
			"ttft",
			ttft ? `${ms(ttft.p50 * 1000)} / ${ms(ttft.p99 * 1000)}` : ABSENT,
			ttft ? `over ${count(ttft.count)} requests` : "no TTFT histogram on this server",
		);
		this.set("inflight", count(status.requests.inFlight), `${count(status.requests.seen)} proxied since start`);
		this.set(
			"slow",
			count(status.requests.slow),
			status.baseline.samples >= 5
				? `median ${ms(status.baseline.medianTotalMs)}`
				: `baseline needs ${5 - status.baseline.samples} more requests`,
		);

		this.root.classList.toggle("degraded", !ok);
	}

	private set(key: string, value: string, note: string): void {
		const stat = this.stats.get(key);
		if (!stat) return;
		stat.value.textContent = value;
		stat.note.textContent = note;
		stat.node.classList.toggle("absent", value === ABSENT);
	}
}
