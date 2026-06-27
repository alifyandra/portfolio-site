# 10. Authentication and session model

Date: 2026-06-20
Status: Accepted

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
