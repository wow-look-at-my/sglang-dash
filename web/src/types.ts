// The wire types, mirroring the Go structs the dashboard's own API serves.

export type Confidence = "measured" | "derived" | "inferred";

export interface Quantiles {
	p50: number;
	p90: number;
	p99: number;
	count: number;
	sum: number;
}

export interface MetricsSnapshot {
	time: number;
	ok: boolean;
	error?: string;
	gauges: Record<string, number>;
	missing: string[];
	quantiles: Record<string, Quantiles>;
}

export interface RequestTotals {
	seen: number;
	retained: number;
	inFlight: number;
	failed: number;
	slow: number;
}

export interface Baseline {
	bestPrefillTokensPerSec: number;
	bestDecodeTokensPerSec: number;
	floorTtftMs: number;
	samples: number;
	medianTotalMs: number;
}

export interface Status {
	mode: "proxy" | "demo";
	upstream?: string;
	uptimeSeconds: number;
	metrics: MetricsSnapshot;
	requests: RequestTotals;
	baseline: Baseline;
	knownSeries: string[];
}

export interface DashEvent {
	seq: number;
	time: number;
	kind: string;
	message: string;
	fields?: Record<string, unknown>;
}

export interface CacheMatch {
	path?: string[];
	reusedNodes?: string[];
	newNodes?: string[];
	expectedCachedTokens: number;
	reportedCachedTokens: number;
	shortfall: number;
}

export interface Cause {
	factor: string;
	ms: number;
	share: number;
	detail: string;
	confidence: Confidence;
}

export interface Diagnosis {
	slow: boolean;
	thresholdMs: number;
	headline: string;
	causes: Cause[];
	notes?: string[];
}

export type RequestStatus = "in_flight" | "done" | "failed" | "aborted";

export interface RequestRecord {
	id: string;
	path: string;
	model: string;
	stream: boolean;
	status: RequestStatus;
	httpCode?: number;
	error?: string;
	arrivedAt: number;
	sentAt: number;
	firstTokenAt?: number;
	finishedAt?: number;
	promptChars: number;
	promptTokens: number;
	cachedTokens: number;
	completionTokens: number;
	promptPreview?: string;
	promptTruncated?: boolean;
	cachePath?: string[];
	cacheMatch: CacheMatch;
	queueDepthAtArrival: number;
	runningAtArrival: number;
	tokenUsageAtArrival: number;
	diagnosis?: Diagnosis;
}

export interface CacheNode {
	id: string;
	parentId?: string;
	depth: number;
	hash: string;
	preview: string;
	chars: number;
	estTokens: number;
	prefixTokens: number;
	hits: number;
	requests: number;
	createdAt: number;
	lastAccess: number;
	inFlight: number;
	children: string[];
	evicted: boolean;
	evictedAt?: number;
	lastRequestId?: string;
}

export interface Eviction {
	time: number;
	nodeIds: string[];
	tokens: number;
	reason: string;
	confidence: Confidence;
	evidence: string[];
	requestId?: string;
	preview?: string;
}

export interface CacheStats {
	nodes: number;
	liveNodes: number;
	evictedNodes: number;
	distinctRoots: number;
	trackedTokens: number;
	totalRequests: number;
	totalHits: number;
	hitTokens: number;
	promptTokens: number;
	tokenHitRate: number;
	charsPerToken: number;
	calibrationSamples: number;
	evictions: number;
	evictedTokens: number;
	lastEvictionAt?: number;
}

export interface CacheSnapshot {
	nodes: CacheNode[];
	roots: string[];
	stats: CacheStats;
	evictions: Eviction[];
	lru: string[];
	redacted: boolean;
}
