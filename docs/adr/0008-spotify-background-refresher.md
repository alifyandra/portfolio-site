# 8. Background refresher for Spotify data (handlers read-only)

Date: 2026-06-13
Status: Accepted

## Context

The Music panel shows live Spotify data (now-playing, top tracks/artists,
playlists). The original handlers were **cache-aside**: each request checked
Redis and, on a miss, called the Spotify API on the visitor's request path, then
wrote the result back with a TTL (30s for now-playing, 1h for the rest).

That bounded Spotify calls to ~one per TTL window, but left two problems:

- **Spotify is on the visitor request path.** The first request after a key
  expires is the one that calls Spotify — that visitor eats the latency, and a
  Spotify outage or 429 surfaces as a 502 *to a visitor*.
- **Cache stampede.** At each expiry tick, several concurrent requests can all
  miss at once and each fire a Spotify call before the first writes back. Load is
  traffic-proportional, which is the opposite of what we want for a third-party
  rate limit we don't control.

## Decision

Split fetching from serving. A **`SpotifyRefresher`** goroutine, started in
`cmd/api` with the server's shutdown context, polls Spotify on a fixed cadence
(now-playing every 30s; the rarely-changing sets once a day) and writes the
results to Redis. The HTTP handlers become **read-only**: they only ever read
the cache and never call Spotify. A cold cache (just after boot) returns an
empty-but-valid body; the panel fills within a tick.

Cache TTLs are set longer than their refresh intervals (now-playing 90s,
others 48h) so a transient Spotify hiccup on one refresh doesn't blank the panel —
the last good value survives until the next success. The TTL is a safety net for
a dead refresher, not the freshness mechanism.

## Consequences

- Visitor traffic is fully decoupled from Spotify: no per-request fetch, no
  stampede, and Spotify load is constant regardless of how many people visit.
- Spotify is polled even when nobody is viewing the site (~2,880 now-playing
  calls/day). Well within Spotify's limits, and acceptable for a single-instance
  deploy; an idle-pause optimisation can come later if needed.
- The refresher runs in-process in the API. Fine for the single `t4g.micro`
  instance (ADR 0006); if the API is ever horizontally scaled, the poller would
  need a leader/lock or to move to the worker so it doesn't run N times.
- Redis stays cache-only, consistent with the existing split (ADR 0007).

## Alternatives rejected

- **SQS / event queue:** a cache refresh is a fixed heartbeat, not a discrete
  event, and SQS has no native scheduling. Wrong tool; would add infra for
  nothing.
- **Single-flight + serve-stale cache-aside:** fixes the stampede and keeps
  calls traffic-proportional, but still couples a (thin) fetch path to visitor
  requests. The poller is simpler to reason about and decouples completely.
- **Run the poller in the worker (`cmd/worker`):** cleaner separation, but the
  API would then depend on the worker being up to have fresh data. Revisit if the
  API is scaled out.
