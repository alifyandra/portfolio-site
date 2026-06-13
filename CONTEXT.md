# Context

Glossary of the domain language for this portfolio site. This file defines
*what words mean*, not how anything is implemented. Keep implementation details
in code and `docs/adr/`.

## Terms

### Project
A piece of work Alif wants to showcase: title, summary, longer description,
tags, external links (repo / live demo), and optional images. Projects are
**dynamic** — stored in the database and served via the API — so they can be
added or edited without a code deploy. Distinct from static résumé content
(experience, education) which is not a Project.

### Contact Message
A message submitted by a site visitor through the contact form: name, email,
and body. Persisted on submission. A Contact Message is *inbound only* — the
site stores it; any reply happens outside the system (e.g. Alif's email).

### Spotify Proxy
A backend-served, cached view of Alif's public Spotify listening data for the
handle `alifyandraid`, exposed as read-only views: **now-playing**,
**recently-played** (fallback shown when nothing is live, so the view is never
dead), **top tracks**, **top artists**, and a **hand-curated playlists** list.
It is a **proxy**: the site never exposes Spotify credentials to the browser;
the backend holds the token, calls Spotify, caches the result, and serves clean
read-only endpoints. Setup, scopes, and the (many) Spotify endpoints that are
dead for this app live in `docs/spotify.md`.

### About Panel
A self-contained personal-interest section rendered in the about page (e.g.
**Music**, **Photography**). An About Panel is *not* a Project (it showcases an
interest, not work) and *not* a Tool (it is read-only, not an app the visitor
operates). Panels are independent of each other and of résumé Static Content;
the about page composes whichever panels exist. Some panels are backed by live
data (Music), others by curated assets (Photography).

### Music
The About Panel showing Alif's listening life, powered by the [Spotify Proxy]
(now-playing with a LIVE badge, top tracks, top artists, curated playlists).
"Music" is the panel; Spotify is its current data source.

### Photography
The About Panel showcasing a curated set of Alif's photos (image + caption).

### Static Content
Résumé-derived material that changes rarely and lives in the frontend codebase,
not the database: hero/about, experience timeline, education, skills, résumé PDF
link. Deliberately *not* modelled as domain entities — putting it behind an API
would add cost with no benefit.

### Job (async)
A unit of background work placed on a queue and processed by a worker out of
band from the web request. The first real Job is **`contact.notify`** — emailing
Alif (via SES) when a Contact Message arrives. Further Jobs (e.g. an LLM-powered
task) slot into the same seam. See `docs/adr/0007-sqs-async-queue.md`.

## Naming conventions

- The owner / subject of the site is **Alif** (Ahmad Alifyandra).
- "The API" / "the backend" = the Go service. "The frontend" = the Next.js app.
- "Prod" = the single EC2 host running docker compose. "Local" = the same
  compose stack on a developer machine.
