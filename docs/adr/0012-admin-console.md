# 12. Admin Console

Date: 2026-07-08
Status: Accepted

## Context

Auth and tiered roles now exist (ADR 10 and its amendments): the backend owns
Google OAuth, resolves an `admin | friend | member` role on every request, and
enforces it server-side. That removes the reason for the standing rule "no public
write endpoints; Projects are seed-only" (CLAUDE.md). The rule predates auth: with
no way to tell Alif apart from an anonymous visitor, the only safe answer was to
allow no writes at all and reseed on deploy. There is now an authenticated admin,
so a write surface restricted to that admin is safe to build, and several pieces of
domain data that currently require a deploy to change should be editable at
runtime.

## Decision

Build an **Admin Console**: an admin-only area at `/admin` in the existing Next.js
app, backed by authenticated write endpoints under `/api/admin/*`.

Access is gated by an **admin-role middleware enforced server-side** on every
`/api/admin/*` operation. The `/admin` route also checks the role in the frontend,
but that gate is UX only (it hides the nav and avoids a flash of the console); the
server middleware is the real boundary, consistent with ADR 10.

The console edits **dynamic domain data without a deploy**:

- **Projects**: full CRUD. This is the first place Projects are writable outside
  the seed. Project images upload **presigned direct-to-S3**: the browser asks the
  backend for a presigned `PUT`, then sends the bytes straight to S3, so file data
  never passes through the `t4g.micro` app host.
- **AccessGrants**: create, list, and remove friend/admin grants (ADR 10
  amendment). This is how a friend is promoted at runtime instead of by editing
  `FRIEND_EMAILS` and deploying.
- **Playlists**: the curated Spotify playlist set behind the Music panel, moved
  from a hardcoded Go const to a DB-curated table so the set can be edited without
  a deploy.

## Deferred

- **A runtime app-friend-only toggle for Tools.** Making a Tool's required tier
  editable at runtime (rather than declared in code) would force a static-to-dynamic
  refactor across the app menu, the landing bench, and the tool cards, for near-zero
  payoff at the current two-to-three Tools. Tools keep declaring their tier in code
  for now.
- **A full admin dashboard.** A richer operational surface, when it is warranted,
  is better built as a **Tool operated with admin privileges** (the same Tool
  pattern as everything else), not as an extension of this console. The Admin
  Console is the lightweight, standardized editor for domain data that predates
  that dashboard, and is deliberately not trying to become it.

## Consequences

- These are the first **admin-role-gated** write endpoints in the system.
  Owner-scoped self-service writes (ADR 10) and friend-gated tool writes (ADR 11)
  already exist; what is new here is an admin writing global, cross-resource domain
  data rather than their own records.
- Presigned direct-to-S3 upload requires **CORS on the S3 bucket** so the browser
  `PUT` is accepted. That ships via a gated Terraform apply and is an **outstanding
  manual step**: browser uploads fail until it is applied.
- The curated playlist set is now **DB-backed**; the Spotify panel reads it from the
  table, and an edit **busts the relevant cache** so the change shows without
  waiting for TTL expiry.
- The Project term finally matches CONTEXT.md: "editable without a deploy" is now
  actually true, where before it was seed-only.
