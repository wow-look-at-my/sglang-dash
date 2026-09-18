// Formatting helpers.

/** The mark for a value the server never reported. */
export const ABSENT = "—";

export function ms(value: number | undefined | null): string {
	if (value === undefined || value === null || !Number.isFinite(value) || value < 0) return ABSENT;
	if (value < 1000) return `${Math.round(value)}ms`;
	if (value < 60_000) return `${(value / 1000).toFixed(value < 10_000 ? 2 : 1)}s`;
	const mins = Math.floor(value / 60_000);
	return `${mins}m ${Math.round((value % 60_000) / 1000)}s`;
}

export function seconds(value: number | undefined): string {
	if (value === undefined || !Number.isFinite(value)) return ABSENT;
	return ms(value * 1000);
}

export function count(value: number | undefined | null): string {
	if (value === undefined || value === null || !Number.isFinite(value)) return ABSENT;
	if (Math.abs(value) < 1000) return `${Math.round(value)}`;
	if (Math.abs(value) < 1_000_000) return `${(value / 1000).toFixed(1)}k`;
	return `${(value / 1_000_000).toFixed(2)}M`;
}

export function percent(fraction: number | undefined | null, digits = 0): string {
	if (fraction === undefined || fraction === null || !Number.isFinite(fraction)) return ABSENT;
	return `${(fraction * 100).toFixed(digits)}%`;
}

export function rate(value: number | undefined, unit: string): string {
	if (value === undefined || !Number.isFinite(value) || value <= 0) return ABSENT;
	return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${unit}`;
}

export function age(at: number | undefined, now = Date.now()): string {
	if (!at) return ABSENT;
	return `${ms(now - at)} ago`;
}

export function clock(at: number | undefined): string {
	if (!at) return ABSENT;
	const d = new Date(at);
	return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${String(d.getMilliseconds()).padStart(3, "0")}`;
}

function pad(n: number): string {
	return String(n).padStart(2, "0");
}

/** Renders an underscored factor or metric key as words. */
export function humanise(key: string): string {
	return key.replace(/[_:]/g, " ").replace(/\s+/g, " ").trim();
}

export function el<K extends keyof HTMLElementTagNameMap>(
	tag: K,
	className?: string,
	text?: string,
): HTMLElementTagNameMap[K] {
	const node = document.createElement(tag);
	if (className) node.className = className;
	if (text !== undefined) node.textContent = text;
	return node;
}

/**
 * A confidence chip. Measured is the signal colour because it came off the
 * server; inferred is amber because it is the dashboard's own reasoning; the
 * rest take the neutral dim chip.
 */
export function confidenceBadge(confidence: string): HTMLElement {
	const badge = document.createElement("scratch-badge");
	badge.setAttribute(
		"variant",
		confidence === "measured" ? "signal" : confidence === "inferred" ? "accent" : "off",
	);
	badge.textContent = confidence;
	return badge;
}

/** A definition row: a label and a value, with the value's provenance. */
export function field(label: string, value: string, confidence?: string): HTMLElement {
	const row = el("div", "field");
	row.append(el("span", "field-label", label), el("span", "field-value", value));
	if (confidence) row.append(confidenceBadge(confidence));
	return row;
}
