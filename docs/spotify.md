# Spotify Proxy — setup & gotchas

How the Music panel's Spotify data works, how to mint creds, and which Spotify
endpoints are dead for this app. See `CONTEXT.md` "Spotify Proxy" for the domain
definition.

## What it serves

Backend proxies Alif's public listening data (token never reaches the browser):

- `GET /api/spotify/now-playing` — live track. When nothing is live it re-shows
  the **last track the poller saw playing** (not the recently-played endpoint, see
  Rate limiting), so the view is never dead. `source` = `now-playing` |
  `recently-played` | `""` (`recently-played` now means "last seen live").
- `GET /api/spotify/top-tracks` — top tracks, `short_term` (~4 weeks).
- `GET /api/spotify/top-artists` — top artists, `short_term`.
- `GET /api/spotify/playlists` — hand-curated list. IDs live in the
  `featuredPlaylistIDs` slice in `backend/internal/api/spotify.go` (edit =
  rebuild). Future: move to DB once auth exists.

All cached in Redis. No creds => endpoints degrade to empty, no error.

## Rate limiting

Spotify's quota is **per app**, shared across all endpoints, on a rolling window.
When an app keeps polling through a 429, Spotify escalates `Retry-After` into the
hours. The refresher (`spotify_refresher.go`) avoids that:

- now-playing polls every **60s** (one call/tick).
- When nothing is live it re-shows the last track it saw playing, instead of a
  second call to the recently-played endpoint. That endpoint is no longer used;
  the old design doubled the idle call volume on the exact path that 429s.
- On a 429 it reads `Retry-After` and **backs off** for that long (honoring the
  header, floored at 5m), keeping the cache warm with the last known track so the
  panel survives even a multi-hour penalty.

## Creds

Three env vars (`.env` locally, prod secrets on EC2):

```
SPOTIFY_CLIENT_ID
SPOTIFY_CLIENT_SECRET
SPOTIFY_REFRESH_TOKEN
```

Client id/secret come from the Spotify app dashboard. Refresh token is **not**
on the dashboard — mint it once (below). It's long-lived (no expiry under normal
use); prod uses the *same* token. Re-mint only if revoked / password changed.

### App settings

- Enable **Web API** only. NOT Web Playback SDK (that needs per-visitor login +
  Premium + a quota-extension review — not worth it for read-only display).
- Redirect URI: `http://127.0.0.1:8888/callback`
  - `localhost` is rejected; must be literal loopback `127.0.0.1`.
  - `http` allowed only for loopback.
  - Used only during the one-time mint; backend never redirects at runtime.

### Scopes (all four — or playlists/artists 403)

```
user-read-currently-playing
user-read-recently-played
user-top-read
playlist-read-private
```

### Mint the refresh token (one-time)

1. Authorize in a browser (fill in client id):

```
https://accounts.spotify.com/authorize?client_id=CLIENT_ID&response_type=code&redirect_uri=http://127.0.0.1:8888/callback&scope=user-read-currently-playing%20user-read-recently-played%20user-top-read%20playlist-read-private
```

Approve. It redirects to `127.0.0.1:8888/callback?code=AQ...` (page fails to
load — fine). Copy the `code` (single-use, ~10 min TTL).

2. Exchange for the refresh token (response `refresh_token` ~131 chars, `AQ...`;
   the `access_token` ~268 chars `BQ...` is the WRONG field):

```
curl -s -X POST https://accounts.spotify.com/api/token \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d grant_type=authorization_code \
  -d code=THE_CODE \
  -d redirect_uri=http://127.0.0.1:8888/callback \
  -u CLIENT_ID:CLIENT_SECRET
```

Put `refresh_token` into `SPOTIFY_REFRESH_TOKEN`.

## Dead endpoints (deprecated for apps created after ~Nov 2024)

Tested 403 / empty on this app — do NOT build features on these:

- **`audio-features`** — 403. No danceability/energy/valence "mood" stats.
- **artist `genres`** — field absent on `top/artists` AND direct `artists/{id}`.
  No genre insights / top-genres.
- **`/users/{id}/playlists`** (public profile playlists) — 403. Can't read
  "what's on my profile"; the per-playlist `public` flag is unreliable too.
  Hence playlists are hand-curated by ID.
- Also gone for new apps: recommendations, related-artists, audio-analysis,
  30s preview URLs.

Rule of thumb: assume any "classic Spotify stats app" trick is dead until you
curl it with a real token. What still works for us: now-playing,
recently-played, top tracks, top artists (names+images, no genres), single
playlist lookup by id.
