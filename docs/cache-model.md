# How the prefix cache view is built

SGLang caches prompt prefixes in a radix tree over tokens. It exposes no endpoint that lists the tree's nodes. So this dashboard reconstructs a view of it from the traffic it proxies. Everything below says which parts are measured and which are inferred.

## What is measured

- The prompt text of each request, read off the request body.
- `prompt_tokens` and the cached-token count from the response's usage block. The OpenAI-shaped path reads `usage.prompt_tokens_details.cached_tokens`. The native path reads `meta_info`. A response carrying neither leaves the count at `-1`, which the UI renders as unreported rather than as a miss.
- Time to first token, for a streamed response.
- Every gauge from the server's own `/metrics`, including `token_usage` and the server's own `cache_hit_rate`.

## What is inferred

**The tree shape.** The dashboard has text, not tokens. It splits each prompt into fixed windows of 256 characters and shares whole windows between prompts. Prompts with the same system message therefore share the chunks that message spans. This is coarser than the server's real token-level matching. That is why a per-node token count is an estimate.

**The chars-per-token ratio.** Each request that reports `prompt_tokens` contributes its character count and its token count to a running total. The ratio is the quotient. It is a measurement of this model's tokenizer on this traffic, not a constant. Until the first request reports a token count, a neutral 4.0 stands in. The panel states how many samples the ratio rests on.

**Which nodes are live.** A node is live until an observation says otherwise. See below.

## How an eviction is noticed

Nothing tells the dashboard that the server dropped a prefix. The dashboard notices it the next time that prefix comes back.

1. A prompt arrives whose leading chunks the tree already holds. The tree predicts how many tokens must therefore be cached.
2. The server reports how many actually were.
3. A shortfall past one chunk of slack means the server is missing part of what the tree holds for that path.
4. The tail of the matched path is marked evicted, deepest chunk first, until the marked tokens cover the shortfall. The server keeps prefixes and drops suffixes, so a shortfall points at the tail.

The slack matters. A chunk boundary never lands on a token boundary. A small shortfall is therefore this model's own granularity rather than an eviction. The floor is one chunk at the calibrated ratio.

## How a reason is chosen

Each eviction carries a reason, a confidence, and the evidence the reason was drawn from. The reader can disagree with the reason without re-deriving the numbers.

| Reason | Chosen when | Confidence |
| --- | --- | --- |
| `server_restart` | a monotonic upstream counter reads lower than the scrape before it | measured |
| `capacity_pressure` | `token_usage` was 0.90 or above | inferred |
| `cache_flush_or_bulk_eviction` | four or more prefixes were lost within ten seconds | inferred |
| `contention_with_running_batch` | `token_usage` was 0.70 or above with a running batch | inferred |
| `unknown` | none of the above, or no metrics were available to classify against | inferred |

## Why there is no TTL

SGLang's radix cache is least-recently-used, not time-to-live. Nothing expires on a clock. A prefix survives until the KV pool is full and it is the coldest thing in it. A countdown will always be fiction. The panel reports the facts that actually decide a prefix's fate instead.

- Idle time, the gap since its last use.
- Eviction rank, its position in the least-recently-used order, coldest first.

A prefix pinned by a live request is marked as such. The server cannot drop what it is serving from.

## Bounds

The tree keeps at most `-max-cache-nodes` nodes, 4000 by default. Past that the coldest childless nodes are forgotten. Forgetting here is the dashboard's own pruning. It publishes no eviction event, because it says nothing about what the server still holds.

## Prompt text

By default the panel shows the head of each chunk. A reader can then tell which prompt a node belongs to. Start with `-redact-prompts` to keep hashes, lengths and token counts and no text at all. The panel then states that text was withheld rather than showing empty rows.
