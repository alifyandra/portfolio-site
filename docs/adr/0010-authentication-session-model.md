# 10. Authentication and session model

Date: 2026-06-20
Status: Accepted (amended 2026-06-28 and 2026-07-08, see Amendments below)

## Context

The site needs authentication. The original framing (CLAUDE.md) treated auth as
a private admin door for Alif only, deferred until a write feature needed it.
We revisited that and decided the other way: the system supports **open
registration** (anyone can sign in with Google and gets a User record), with
Alif distinguished by an admin role. No visitor-facing feature consumes member
accounts yet; we are building the mechanism now and accepting that members
currently own nothing.

The frontend is Next.js on Vercel (`aliflabs.dev`); the backend is Go on EC2
(`api.aliflabs.dev`). These are the same registrable domain, so a cookie scoped
to `.aliflabs.dev` is shared across both and is "same-site" for CORS/SameSite
purposes.

## Decision

**The Go backend owns the entire OAuth flow and the session.** It runs the
Google authorization-code dance with `golang.org/x/oauth2`, verifies Google's
ID token, upserts the User and Identity, and sets an **opaque, server-side
session** cookie. The session of record is a Postgres row (Ent `Session`);
the cookie carries only a random token (stored hashed at rest).

Domain model is split three ways:

- **User** is the person: canonical `email` (unique), `name`, `avatar_url`,
  `role`, timestamps.
- **Identity** is one external login credential: `(provider, provider_sub)`
  unique, FK to User, plus the provider-asserted email. The durable key is the
  provider `sub`, never the email.
- **Session** is one logged-in device: hashed token, FK to User, `expires_at`.

Policy choices:

- **Roles**: `admin` and `member`. Admin is conferred by an env email allowlist
  (`ADMIN_EMAILS`), matched against Google's `email_verified` email and
  re-asserted on every login, so a stray DB edit self-heals and a fresh prod DB
  needs no manual step.
- **Session**: 30-day sliding expiry, revocable per device, with a
  "log out everywhere" that deletes all of a user's sessions.
- **Cookie**: `HttpOnly; Secure; SameSite=Lax; Domain=.aliflabs.dev` (`Secure`
  is dropped only in local dev over http). CSRF is handled by SameSite=Lax plus
  a strict exact-origin CORS allowlist with credentials enabled.
- **Lifecycle**: self-service hard delete (`DELETE /api/auth/me`) cascading to
  Identities and Sessions, guarded so the sole admin cannot delete himself.

## Consequences

- CORS flips from `AllowCredentials: false` to `true`, and the allowed-origins
  list must stay exact (no wildcards) for credentialed requests to work.
- Authorization is enforced server-side by Huma middleware that resolves the
  cookie to a User and puts it on the request context. A cookie security scheme
  is registered so protected operations appear correctly in `/docs`. Frontend
  route-guarding is UX only, never the real gate.
- Auth degrades gracefully without Google credentials, matching the house
  pattern (Spotify/SES/queue): the app still boots, and the auth endpoints
  report "not configured" rather than crashing.
- Account-linking across providers is deferred. With Google as the only provider,
  one Google `sub` maps to one Identity maps to one User; linking a second
  provider to an existing User is a future decision (and a known security
  footgun if done naively by email).
- This supersedes the "auth deferred / admin-only" notes in CLAUDE.md.

## Alternatives rejected

- **Single-admin auth** (allowlist Alif only, no member accounts): simpler and
  matched the original framing, but rejected in favour of building the
  multi-user mechanism now. The OAuth mechanism is identical either way; only
  the sign-in policy differs.
- **Auth.js / NextAuth on the frontend**: splits auth ownership across two
  stacks and two deploy targets and forces user-sync into Postgres, breaking the
  "backend is the single source of truth" architecture.
- **Stateless JWT sessions**: avoids a per-request lookup the site does not need,
  but makes revocation and role changes lag until expiry and adds signing-key
  rotation. A `JWT + refresh token` variant was rejected as pure overkill.
- **Sessions in Redis**: Redis is cache-only and flushable per ADR 0007; using
  it as the session of record would log everyone out on a flush.
- **Email as the identity key**: emails are mutable and reusable, which enables
  account orphaning and hijack. Keyed on the Google `sub` instead.

## Amendment (2026-06-28): tiered access via a friend role

The two-role model (`admin`, `member`) is extended to three: `admin`, `friend`,
`member`. The `friend` tier sits between the other two and is conferred by a
`FRIEND_EMAILS` allowlist, matched on every login exactly like `ADMIN_EMAILS`,
so it self-heals against a stray DB edit and needs no manual step on a fresh DB.
Precedence is admin, then friend, then member: an email on both allowlists
resolves to admin.

Open registration is unchanged: anyone who signs in with Google still gets a
User record. What the tiers gate is tool access, not account creation. Each tool
declares the tier it needs; the launcher shows each person the set their tier
allows, and the backend enforces the requirement per tool (frontend filtering is
UX only). The first gated tool is the friends-only WhatsApp sender; public tools
stay open to any signed-in member.

This supersedes the binary admin/member framing in the Decision above. The
session, cookie, and OAuth machinery are untouched; only the `User.role` enum
and the role-resolution step changed. The enum and the API `role` field now
carry `admin | friend | member`.

## Amendment (2026-07-08): Nickname and Welcome state on the User

The WhatsApp Sender (ADR 11) already introduced an authenticated, owner-scoped
self-service write — `PATCH /api/auth/me`, which lets a signed-in User set their
own `default_country_code`. The Welcome / Nickname feature (see CONTEXT.md) gives
the User two more pieces of self-owned state, so this amendment **extends that
existing endpoint and the `User` entity** rather than introducing a new pattern.
(It is therefore *not* the first authenticated mutation — the earlier "no *public*
write endpoints" stance is about anonymous writes and is untouched.)

Two nullable columns are added to `User`:

- **`nickname`** (string, nullable) — a self-chosen display name. Optional; when
  set it overrides the provider `name` for display, with precedence
  `nickname ?? name ?? email`. The provider `name` is never overwritten (it stays
  the canonical, re-asserted-each-login value); `nickname` is the vanity layer.
- **`greeted_role`** (role enum, nullable) — the `Role` at which the User was last
  shown the full Welcome; `null` means never welcomed. This is the authoritative,
  cross-device record that drives whether a sign-in plays the full first-time
  greeting, the short returning greeting, or the "you are my friend" **promotion**
  celebration (shown when the current role has risen above `greeted_role`). It is
  server-owned state, not client-supplied.

The same endpoint — **`PATCH /api/auth/me`** (operationId `update-current-user`) —
gains the two fields. It stays session-authenticated and **owner-scoped by
construction**: it mutates the User resolved from the session cookie, with no user
id in the path, so editing another User is not expressible. Any signed-in User
(member/friend/admin) may call it for their own record; it is profile self-service,
not a role-gated capability. The request body now carries (alongside the existing
`default_country_code`, which is relaxed to optional so partial updates are valid):
`nickname` — tri-state, distinguishing absent (leave as-is) from explicit `null`
(clear) from a value (validated: trimmed, 1–40 runes, control chars stripped) — and
an `ack_welcome` flag that sets `greeted_role` to the caller's **current** role
server-side (the client cannot assert an arbitrary role). It returns the updated
User — the same body as `GET /api/auth/me`, which now also carries `nickname` and
`greeted_role`.

Consequences: an additive Ent migration (two nullable columns, no backfill, applied
automatically by the boot-time `Schema.Create`), regenerated OpenAPI + frontend
hooks, and a backend deploy. The greeting choice is computed on the client from
`role` vs `greeted_role`, but both values are server-authoritative; the Welcome
remains UX only and never substitutes for the server-side role checks that gate
Tools.

## Amendment (2026-07-08): role source of truth is max(env allowlist, DB grant)

The 2026-06-28 amendment conferred tiers purely from the env allowlists
(`ADMIN_EMAILS` / `FRIEND_EMAILS`), re-asserted on every login. That is the right
bootstrap but the wrong ceiling: promoting a friend meant editing an env var and
deploying. The Admin Console (ADR 12) needs to grant `friend` (and, rarely,
`admin`) at runtime, so role gains a second, database-backed source.

**New entity: `AccessGrant`.** One row per pre-authorized person: `email`
(normalized to lowercase, unique) and `tier` (`admin | friend`). It is keyed by
**email, not User**, so a person can be granted a tier **before they have ever
signed in**; the grant is matched at their first login, exactly as the env
allowlists are. There is no `member` grant, because `member` is the floor every
signed-in User already has, so a grant only ever raises a role above it.

**Resolution: role is the max of the two sources.** On every login the backend
computes the env-derived tier (as before) and the DB-grant tier (by normalized
email) and takes the **higher** of the two, on the ordering `admin > friend >
member`. The env allowlists therefore stay a permanent **floor**: a grant can only
**raise** a role, and absence from the DB never **lowers** an env-allowlisted user.
Concretely, the env-listed admin cannot lock himself out by deleting or downgrading
his own `AccessGrant`; his floor is `admin` regardless of the table.

**Writes.** Removing a grant drops the person back to their env-derived tier at
their next login (for the env admin that is still `admin`; for everyone else,
`member`). Because role is otherwise only recomputed at login, an admin write to a
grant **also eagerly updates the stored `User.role`** for a User who already exists,
so a promotion or demotion takes effect immediately instead of waiting for that
person's next sign-in. A grant whose email has no User yet simply waits and is
applied at first login.

**Why max/floor rather than the DB owning role outright.** Three reasons.
(1) **Lockout safety**: the env allowlist is seeded from Terraform/SSM and is the
one source no in-app edit can corrupt, so keeping it as a floor guarantees the
operator always retains admin even after a bad grant edit or an empty/restored
table. (2) **Pre-authorization by email**: grants can name people who have never
signed in, which a User-FK'd role column cannot express. (3) **Bootstrap stays
authoritative**: a fresh prod DB still resolves the admin correctly from env with
no manual step, preserving the self-healing property the original Decision relied
on. Pure-DB ownership of role would give none of these and would turn the
allowlists into dead config.

The session, cookie, and OAuth machinery are unchanged; only role resolution gains
the second source. The `User.role` enum and the API `role` field still carry
`admin | friend | member`.
