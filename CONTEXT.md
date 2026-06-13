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
A backend-served, cached view of Alif's public Spotify listening data
(now-playing / top tracks) for the handle `alifyandraid`. It is a **proxy**: the
site never exposes Spotify credentials to the browser; the backend holds the
token, calls Spotify, caches the result, and serves a clean read-only endpoint.

### Static Content
Résumé-derived material that changes rarely and lives in the frontend codebase,
not the database: hero/about, experience timeline, education, skills, résumé PDF
link. Deliberately *not* modelled as domain entities — putting it behind an API
would add cost with no benefit.

### Job (async)
A unit of background work placed on a queue and processed by a worker out of
band from the web request. None exist on day one; the queue wiring is laid so
the first real Job (e.g. an LLM-powered task, or sending contact notifications)
can be added without re-architecting. See `docs/adr/0007-sqs-async-queue.md`.

## Naming conventions

- The owner / subject of the site is **Alif** (Ahmad Alifyandra).
- "The API" / "the backend" = the Go service. "The frontend" = the Next.js app.
- "Prod" = the single EC2 host running docker compose. "Local" = the same
  compose stack on a developer machine.
