# sglang-dash

An observability dashboard for an SGLang server. One Go binary with the UI embedded by `go:embed`.

## Build and test

- `npm --prefix web ci && npm --prefix web run build` writes `web/dist`, which `main.go` embeds. It is committed, and CI fails on a difference between it and what `web/src` builds.
- `go-toolchain` in the repo root is the whole Go gate. Never run a bare `go` command.
- The UI is checked in a real browser, not with curl. A js-snippets module import that fails leaves a blank page and no HTTP error.

## Layout

- `main.go` — flags, wiring, the embedded UI, and the routes handed to the proxy.
- `internal/upstream` — the recording reverse proxy and the metrics scraper.
- `internal/metrics` — Prometheus text parser, series history, histogram quantiles.
- `internal/cache` — the reconstructed prefix tree and the eviction detector.
- `internal/diagnose` — the learned baseline and the latency attribution.
- `internal/requests`, `internal/events` — the request ring and the event bus.
- `internal/api` — the `Hub` every collector writes into, plus the HTTP surface.
- `internal/demo` — the traffic simulator behind `-demo`.
- `web/src` — the UI. Components come from js-snippets as runtime URL imports.

## Invariants

- A value the server never reported is never rendered as 0. A cached-token count stays at `-1` until the server supplies one. A missing gauge is listed in `Snapshot.Missing`. A failed scrape clears the gauges rather than keeping the last good ones.
- Every claim about the prefix cache carries a confidence. Token counts from the server are `measured`. The tree shape and every eviction reason are `inferred`, and each eviction carries the evidence its reason was drawn from.
- The latency attribution always sums to the wall time. Whatever the factors cannot place is reported as `unattributed`.
- Every JSON slice is serialised as an array, never null. A null where the UI expects a list breaks the panel on an idle server.
- The proxy never delays a client. A streamed chunk is written and flushed before it is parsed.

## Documentation

- `docs/architecture.md` — the collectors, how the browser is updated, the asset build.
- `docs/cache-model.md` — what is measured, what is inferred, how an eviction is noticed.
- `docs/slow-attribution.md` — the baseline and each latency slice.
- `docs/metrics.md` — the upstream metric names and the events the scraper raises.
