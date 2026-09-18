# The upstream metrics it looks for

The scraper parses whatever the server's Prometheus endpoint serves and keeps a bounded history of every series in it. On top of that raw store it maps a small set of dashboard fields onto the metric names SGLang publishes. The mapping lives in `metrics.Gauges` and `metrics.Histograms`.

## Why a field lists several names

SGLang has renamed metrics between releases. A field therefore lists every spelling it knows, tried in order, and takes the first one the server actually exports. A field whose spellings are all absent is reported in `Snapshot.Missing`. The panel then says the server does not export it, in place of the trace.

That distinction is the point. A flat line at zero and a metric that does not exist look identical on a chart and mean opposite things. The same rule covers a failed scrape. The snapshot carries no gauge values at all, rather than the last good ones. An outage therefore reads as an outage.

## Gauges

| Field | Metric names tried |
| --- | --- |
| `running_reqs` | `sglang:num_running_reqs`, `sglang:num_running_requests` |
| `queued_reqs` | `sglang:num_queue_reqs`, `sglang:num_waiting_requests` |
| `used_tokens` | `sglang:num_used_tokens` |
| `token_usage` | `sglang:token_usage` |
| `gen_throughput` | `sglang:gen_throughput` |
| `cache_hit_rate` | `sglang:cache_hit_rate` |
| `prompt_tokens` | `sglang:prompt_tokens_total` |
| `generation_tokens` | `sglang:generation_tokens_total` |
| `requests_total` | `sglang:num_requests_total` |
| `aborted_total` | `sglang:num_aborted_requests_total` |
| `grammar_queue` | `sglang:num_grammar_queue_reqs` |
| `spec_accept_len` | `sglang:spec_accept_length` |

A field with several matching series, one per label set, is summed. A per-model deployment therefore reports its total, and the raw per-label series stay available through `/api/metrics/series`.

## Histogram families

| Field | Metric families tried |
| --- | --- |
| `ttft` | `sglang:time_to_first_token_seconds` |
| `tpot` | `sglang:time_per_output_token_seconds`, `sglang:inter_token_latency_seconds` |
| `e2e_latency` | `sglang:e2e_request_latency_seconds` |
| `queue_time` | `sglang:waiting_latency_seconds`, `sglang:queue_time_seconds` |
| `prefill_time` | `sglang:prefill_latency_seconds` |

Quantiles are read off the cumulative `_bucket` counts. The value is interpolated inside the bucket that crosses the target rather than reported as that bucket's upper bound. An un-interpolated read pins p99 to a bucket edge, and a latency change then stays invisible until it crosses one.

## What the scraper reports on its own

The scraper watches the series it already has and publishes an event when one of them changes state.

- `upstream.unreachable` and `upstream.reachable`, on the first failed scrape and the first recovered one.
- `cache.flushed`, when a monotonic counter reads lower than the scrape before it. Only a restart does that. A restarted server has an empty prefix cache.
- `scheduler.stalled` and `scheduler.recovered`, when requests are running with zero generation throughput for longer than a few scrapes. One quiet tick between batches is normal. A lone tick is never reported.
- `memory.pressure`, when `token_usage` reaches 0.95. It clears at 0.85. The gap between the thresholds is what stops a value hovering on the line from filling the feed.

## Adding a metric

Add the name to the right map in `internal/metrics/store.go`. A gauge needs a spec in `web/src/gauges.ts` to get its own strip. Nothing else needs to change, because every scraped series is already stored and already listed in `/api/status` under `knownSeries`.
