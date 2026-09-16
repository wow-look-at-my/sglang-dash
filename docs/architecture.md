# Architecture

The binary is one HTTP server. It serves the dashboard, and it proxies everything else to the SGLang server.

## The collectors

```
client ──▶ proxy ──▶ SGLang server
             │
             ├─▶ requests.Store   one record per request, with its timings
             ├─▶ cache.Tree       the reconstructed prefix tree
             ├─▶ diagnose         the latency attribution and its baseline
             └─▶ events.Bus       the activity feed, fanned out over SSE
                   ▲
scraper ───────────┘   metrics.Store, polled from /metrics on a fixed cadence
```

- `internal/upstream` holds the proxy and the metrics scraper. The proxy reads each request body to find the prompt, forwards the body unchanged, and reads the response's usage block on the way back. A streamed response is relayed line by line and flushed before it is parsed, so the dashboard never delays a client's tokens.
- `internal/metrics` parses the Prometheus text format and keeps a bounded history per series. It also estimates quantiles from cumulative histogram buckets, with interpolation inside the winning bucket.
- `internal/cache` reconstructs the prefix tree. See [cache-model.md](cache-model.md).
- `internal/diagnose` learns the deployment's best observed rates and splits each request's wall time against them. See [slow-attribution.md](slow-attribution.md).
- `internal/events` is a ring buffer plus a fan-out. A subscriber whose buffer is full misses events rather than blocking the proxy path.
- `internal/api` owns the `Hub`, which implements the proxy's `Recorder` interface. Every collector is written from there, and every handler reads from there.
- `internal/demo` fabricates traffic and the metrics that accompany it. It writes real exposition text through the real parser, so the demo exercises the same code path as a live server.

## Two update paths in the browser

Status frames and events are pushed over `/api/stream`. They drive the header, the gauges and the activity feed. The request list and the cache snapshot are pulled every 1.5 seconds, because both are whole collections. A delta protocol for them buys nothing at this size.

## Where a missing number goes

A gauge the server does not export is reported in `Snapshot.Missing`. The panel then says so in place of the trace. A failed scrape clears the gauges rather than keeping the last good values. Both rules exist for the same reason. A flat line at zero and a metric that does not exist look identical on a chart and mean opposite things.

## Assets

`main.go` embeds `web/dist` with `go:embed`. The UI source lives in `web/src` and is compiled by esbuild through `web/build.mjs`. The js-snippets components stay as runtime URL imports. The browser fetches those from the library site, so an upstream fix reaches this dashboard without a re-vendor here.
