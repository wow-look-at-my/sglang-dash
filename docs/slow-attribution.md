# How a request's latency is split

Open a row in the requests table and the dashboard shows where that request's wall time went. This page says how each slice is produced, and how much of it is measurement.

## The baseline

Nothing here compares against a constant. A fixed "fast" number will make every request on a modest GPU look like an incident. Instead the dashboard learns this deployment's own best case from the requests it has already seen.

- `bestPrefillTokensPerSec` is the fastest uncached-prompt-token rate observed during a pre-first-token phase.
- `bestDecodeTokensPerSec` is the fastest output-token rate observed.
- `floorTtftMs` is the shortest first-token time seen on a request with no uncached prompt tokens at all. That is the fixed cost of a round trip through this stack.
- `medianTotalMs` is the rolling median wall time over a bounded window of recent completed requests.

Only a streamed response sets the prefill and decode baselines. A non-streamed response never puts a first-token time on the wire, so its timings cannot teach the baseline anything about prefill.

The baseline travels with every diagnosis as a sample count. Below the minimum, `diagnose.minBaselineSamples`, the answer says so in its own notes. It calls nothing anomalous on the strength of one lucky request.

## The slices

| Slice | How it is produced | Confidence |
| --- | --- | --- |
| `proxy_overhead` | arrival to the moment the body reached the server, off the dashboard's own clock | measured |
| `round_trip_floor` | the learned `floorTtftMs`, capped at the phase it is being taken out of | inferred |
| `prefill_uncached_tokens` | uncached prompt tokens divided by the best observed prefill rate | inferred |
| `queue_wait` | whatever is left of the pre-first-token phase after the floor and the prefill estimate | inferred |
| `decode` | output tokens divided by the best observed decode rate | inferred |
| `decode_contention` | the real decode span minus that ideal | inferred |
| `generation` | the whole server-side span, for a non-streamed response where prefill and decode cannot be separated | measured |
| `unattributed` | the wall time no slice above accounts for | derived |

`unattributed` exists so the slices always add up to the wall time. A model that quietly drops its remainder hides exactly the case worth a look.

## What the notes add

A diagnosis carries notes alongside its slices.

- A prefix shortfall, when the reconstructed tree expected more cached tokens than the server reported. That is why the prefill was longer than the prompt's novelty suggests.
- KV usage at 90% or above on arrival, which means the server had little room to admit the request.
- A provisional baseline, when too few requests have completed for the comparisons to mean much.
- A non-streamed response, which is why there is one opaque `generation` slice instead of a split.

## When a request is called slow

A request is slow when its wall time reaches the threshold. The threshold is the larger of `-slow-floor-ms`, 2000 by default, and `-slow-factor` times the rolling median, 2.5 by default. Without the floor, a fast deployment marks every request that sits a little above a 40ms median. Without the factor, a slow deployment marks nothing at all.

Each slow request publishes a `latency.slow_request` event naming its largest slice. The activity feed then carries the reason and not only the fact.
