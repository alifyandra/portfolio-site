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
dead), **top tracks**, **top artists**, and an **admin-curated playlists** list
(the set is managed in the [Admin Console], not hardcoded).
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

### Tool
An app a [User] operates on the platform, distinct from a read-only [About Panel]
and from a [Project] (which is portfolio work). Tools may be gated by [Role]. The
first Tool is the **WhatsApp Sender** (friend-gated): a friend links their own
WhatsApp by QR, stores message templates and recipient lists, and triggers
personalized batch sends. Its sending engine runs in a separate, private
whatsapp-web.js sidecar service, off the main host. See ADR 11. Each Tool belongs
to a [Category] for grouping in the app menu.

### Category
A lightweight, organizational grouping of related [Tool]s in the app menu (e.g.
**Messaging**, **Utilities**, **Experiments**). Purely a navigation label — a
Category confers no capability and gates nothing; access is always decided by
[Role]. Categories have a defined display order, and empty ones are hidden.

### Welcome
The first-sign-in moment for a [User]: a one-time prompt asking what they'd like to
be called (setting their [Nickname]; skippable, in which case the Google `name` is
adopted), followed by an animated greeting. The greeting is [Role]-aware — a
**friend** or **admin** gets an extra "you are my friend" beat that a **member**
does not. The full greeting is shown on first sign-in, and again the first time a
User signs in **after their [Role] rises to friend/admin** — so a newly-promoted
friend gets the celebratory "you are my friend" reveal even though they are a
returning User. Otherwise later sign-ins replay a short greeting, and the Nickname
is never re-asked after the first time. The Welcome is UX, never an authorization
boundary (the server still enforces [Role] independently).

### Static Content
Résumé-derived material that changes rarely and lives in the frontend codebase,
not the database: hero/about, experience timeline, education, skills, résumé PDF
link. Deliberately *not* modelled as domain entities — putting it behind an API
would add cost with no benefit.

### Visitor
Anyone using the site without having authenticated. Visitors are anonymous: they
read Projects, view About Panels, and submit Contact Messages. The default
audience of the site. Contrast with [User].

### User
A person who has authenticated. A User has a canonical `email`, provider-asserted
display `name`, an optional self-chosen [Nickname], avatar, a `role` (see [Role]),
and one or more [Identity]s. Distinct from a [Visitor] (anonymous) and from
**Alif** (the site's owner, who is *a* User with the admin role). Anyone may become
a User by signing in (open registration); having a User record does not by itself
grant any elevated capability.

### Nickname
A short display name a [User] chooses for themselves ("what they'd like to be
called"), distinct from the provider-asserted `name`. Optional; when set it
**overrides** `name` wherever the User is shown (navbar, greeting, account). The
`name` is never overwritten and remains the canonical fallback — display precedence
is `nickname ?? name ?? email`. Unlike `name` and `role` (re-asserted from Google /
env allowlists on every login), a Nickname is User-owned and only the User changes
it. Collected on first sign-in (see [Welcome]) and editable afterward.

### Identity
A single external login credential belonging to a [User]: a `(provider,
provider_sub)` pair (e.g. Google + the stable `sub` claim) plus the email that
provider asserts. A User may have several Identities (one per login method);
each Identity belongs to exactly one User. The durable key is the provider's
`sub`, never the email (emails are mutable and reusable).

### Role
The authorization level of a [User]. Three tiers exist: **admin** (Alif: full
read/write over Projects, Photography, playlists, and the admin area), **friend**
(allowlisted users who may use friends-only [Tool]s such as the WhatsApp Sender),
and **member** (every other signed-in User: no capabilities beyond being signed
in). admin and friend are conferred two ways, combined as the *higher* of the
two on every login: env allowlists (`ADMIN_EMAILS`, `FRIEND_EMAILS`), which act
as a permanent floor a User cannot be dropped below, and admin-issued [Access
Grant]s stored in the database. admin takes precedence over friend. Roles gate
*what a User may do*; authentication only proves *who they are*. See ADR 10
(amended).

### Access Grant
An admin-issued authorization record, keyed by **email**, that confers a [Role]
tier (friend or admin) on whoever signs in with that email — even before they
have ever signed in. Access Grants are the database-backed, deploy-free way
Alif manages who is a friend, complementing the env allowlists. On login a
User's effective Role is the *higher* of their env-allowlist tier and any Access
Grant for their email, so a grant can only *raise* a Role and the env allowlist
stays a floor (Alif cannot lock himself out of admin by editing grants).
Removing a grant drops the User to whatever the allowlists confer at their next
login. See ADR 10 (amended).

### Admin Console
The admin-only area (`/admin`) where Alif edits the site's *dynamic* domain data
without a deploy: [Project]s, [Access Grant]s, and the curated playlists shown
in the [Music] panel. It is site chrome, not a [Tool] — reached from a navbar
link shown only to admins and gated by the admin [Role] (re-enforced on the
server, never trusting the client gate). Distinct from a possible future admin
*dashboard*, which would be a full [Tool] operated with admin privileges; the
Admin Console is the lightweight, standardised editor that predates it.

### Job (async)
A unit of background work placed on a queue and run out of band from the web
request. A Job is triggered either by an **event** (a producer reacting to
something, e.g. a Contact Message produces `contact.notify`, which emails Alif
via SES) or by a **schedule** (the clock, e.g. a nightly `digest.build`). The
worker consumes the queue and **dispatches** each Job by weight: light Jobs it
runs inline on the host; heavy Jobs it launches on a dedicated Fargate task that
does the work and then exits (run to completion). Distinct from a [Batch], which
is user-triggered and watched live. See `docs/adr/0007-sqs-async-queue.md` and
`docs/adr/0013-scheduled-heavy-compute-jobs.md`.

### ScheduledJob
The definition of a recurring [Job (async)]: a stable `key` (e.g. `digest.scrape`),
whether it is `enabled`, its cron `schedule` and timezone, which kind of [Runner]
should execute it, and the bookkeeping for when it last ran and runs next. Stored in
the database and edited from the [Admin Console], so the schedule and the on/off
switch are runtime data rather than Terraform. The in-process scheduler in the worker
reads these rows on a tick and enqueues a run when one is due, which is what replaced
the EventBridge schedule for these jobs. See ADR 13 (amended) and ADR 14.

### JobRun
One execution of a [ScheduledJob]: its status (queued, running, succeeded, failed,
cancelled), whether it was triggered by the schedule or forced manually, which
[Runner] ran it, the scheduled time it represents, timing, and any error or stats. A
unique `(job, scheduled_for)` pairing is what makes the schedule idempotent, so a
double tick or a redelivered queue message collapses to a single JobRun. The run
history shown in the [Admin Console] is a list of JobRuns. See ADR 14.

### Artifact
A stored piece of a [Job (async)]'s input or output, kept so a later stage or a
[Runner] can consume it without redoing earlier work. The [Digest]'s scrape stage
writes one Artifact per [Source]; the LLM stage reads them. An Artifact is stored
inline in the database when small and in S3 when large, and expires after about a
week. It exists for three reasons: to retain what was scraped, to let the LLM stage
be re-run over stored input without re-scraping, and to buffer scraped data for an
external [Runner] to claim (the future home-finance hand-off). See ADR 14.

### Runner
Where a [Job (async)]'s work actually executes. Three kinds: **server** (the worker
on the box, or a Fargate task it launches), **local**, and external runners that
**pull** work over HTTPS instead of being pushed to, namely Alif's laptop running
Claude Code today and a home finance-scraper server later. An external Runner claims
a job's [Artifact]s via the work API and posts results back, so the connection is
always outbound from the runner ("home polls AWS"), never inbound. An external Runner
authenticates with an [API Token]. See ADR 14.

### API Token
A scope-only bearer credential that lets an external [Runner] use the work API
(`/api/work/*`) without a login session. It resolves to a [User] and a scope on a
request-context key separate from the session cookie's, and it gates only the work
API, never the [Admin Console] under `/api/admin/*`. Stored as a hash, one token per
runner (`laptop`, `home-finance`), each independently revocable, so a leaked token
can only claim and complete the work its scope names. Distinct from the auth session
that proves who a person is in the browser (ADR 10): an API Token authorizes a
machine to pull job work. See ADR 14.

### Digest
A dated, machine-generated summary of a fixed set of public [Source]s, produced
by the scheduled `digest.build` [Job (async)] and stored for later reading. It is
the first scheduled Job that fetches external data and summarizes it with an LLM,
the same shape the future personal dashboard's periodic briefings will follow.

### Source
A public origin the [Digest] job reads from, such as an RSS feed or a web page.
Sources are read-only and unauthenticated (no OAuth), which is what keeps the
[Digest] job free of any per-user credential handling.

### Template
A reusable WhatsApp message body owned by a [User], optionally containing a
`{name}` placeholder substituted per recipient at send time. One of the two
durable inputs to a [Batch] in the WhatsApp Sender [Tool]. See ADR 11.

### Recipient List
A named, [User]-owned set of [Recipient]s; the other durable input to a [Batch].
Recipient data is PII — it lives only in the database, never in git.

### Recipient
One entry in a [Recipient List]: a phone number in canonical international form
(digits only, no `+`, no leading zero) plus an optional name. Numbers are
normalized on entry against a default country code (a bare leading `0` is read as
that country's local number).

### Batch
One send run of the WhatsApp Sender: a [Template] applied to a [Recipient List],
carrying a status and aggregate counts (sent / skipped / failed). Distinct from a
[Job (async)] — a Batch is a [User]-triggered, role-gated send watched live, not a
queued background task. _Avoid_: blast, campaign.

### Link
The ephemeral, QR-established connection to a friend's own WhatsApp, held only by
the private sidecar for the lifetime of a single [Batch] and destroyed afterward;
never persisted server-side. Distinct from the auth session that proves a [User]'s
identity (ADR 10): a Link connects WhatsApp, the auth session proves who you are.
_Avoid_: session.

## Naming conventions

- The owner / subject of the site is **Alif** (Ahmad Alifyandra).
- "The API" / "the backend" = the Go service. "The frontend" = the Next.js app.
- "Prod" = the single EC2 host running docker compose. "Local" = the same
  compose stack on a developer machine.
