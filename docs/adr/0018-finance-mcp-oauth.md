# ADR 0018: OAuth 2.1 authorization server for the finance MCP connector

Status: Accepted (held behind a flag, off by default)
Date: 2026-07-26
Relates to: ADR 0010 (auth / session model), ADR 0014 (scope-only bearer work API),
ADR 0017 (finance read service + remote MCP server).

## Context

The remote MCP server at `/mcp` (ADR 0017) authenticates with a single static
`finance.read` bearer token. That works for a CLI the owner controls (Claude Code
pastes the token), but the claude.ai web and mobile apps do not accept a pasted
bearer for a remote MCP connector: they run an OAuth 2.1 handshake. To connect the
finance MCP server from claude.ai, the origin has to BE an OAuth authorization
server and a spec-compliant resource server: serve discovery metadata, run an
authorization-code + PKCE flow, and mint audience-bound access tokens.

The site already owns a Google login and admin-tiered sessions (ADR 0010). Only one
person (the admin owner) should ever connect their own finance data. So this is not
a general multi-user OAuth provider: it is a single-owner authorization server whose
consent step is gated on the existing admin session.

## Decision

Be our own authorization server; do NOT proxy Google. claude.ai only ever talks to
this origin. The browser consent step reuses the backend-owned Google login to
establish who is at the keyboard, but Google is never exposed to Claude as an OAuth
AS.

### Surface (`internal/oauth`, raw Chi routes alongside `/mcp`)

- `GET /.well-known/oauth-protected-resource` (+ `/mcp` suffix variant): RFC 9728
  metadata pointing Claude at this AS.
- `GET /.well-known/oauth-authorization-server`: RFC 8414 metadata. PKCE `S256`
  only; `token_endpoint_auth_methods_supported: ["none"]` (public client).
- `GET /oauth/authorize`: validates the request, resolves the session, and renders
  a CSRF-protected consent page for an ADMIN session only. It never mints a code.
- `POST /oauth/authorize`: the explicit approve. Re-validates everything, checks the
  CSRF double-submit token, and mints a one-time PKCE code.
- `POST /oauth/token` (form-encoded only): `authorization_code` (PKCE verify) and
  `refresh_token` (rotating) grants.

### Single public client, exact redirect allowlist

One hardcoded public `client_id` (`aliflabs-finance-mcp`); no dynamic client
registration (claude.ai is configured with the id in the connector's Advanced
settings). `redirect_uri` is matched exactly for the claude.ai callback
(`https://claude.ai/api/mcp/auth_callback`) and by host for http loopback (Claude
Code and other local clients, RFC 8252): loopback is only reachable on the user's
own machine, so any port is safe. Everything else is refused, so there is no open
redirect.

### Security posture

- PKCE `S256` is required; a missing or non-S256 challenge is rejected at authorize,
  the verifier is checked at token.
- Owner-only: only an `admin`-role session can approve. A member/friend session is
  refused outright (a 403 page, not a login redirect, so there is no loop);
  anonymous is sent through Google login with a same-origin-only `return_to`.
- Session is not consent: a code is only minted by an explicit POST carrying a valid
  CSRF token, so a logged-in browser cannot be made to yield a code by a forged
  cross-site request.
- Audience binding: every token is stamped with the MCP URL as its audience. `/mcp`
  rejects any token whose audience is not this resource. There is no token
  pass-through.
- Codes are one-time and expire in ~60s; access tokens last ~1h; refresh tokens
  rotate on every use (the presented refresh dies, a successor is issued), so a
  replayed refresh is dead. Revoke is a row-state change.
- Codes and tokens are stored as SHA-256 hashes only (two Ent entities,
  `OAuthAuthCode` and `OAuthToken`), mirroring `ApiToken` and `Session`. Secrets are
  never logged.

### Resource server keeps the static bearer

`/mcp` accepts EITHER a valid static `finance.read` `ApiToken` (unchanged, Claude
Code) OR a live audience-bound OAuth access token. It never accepts a session
cookie. The static path works in both flag modes.

### Feature flag, held off

The whole surface is behind `FINANCE_MCP_OAUTH_ENABLED` (default false). Off: the
discovery + `/oauth/*` routes 404 and the `/mcp` 401 challenge stays the legacy
bearer-only form. On: the `/mcp` challenge advertises the resource-metadata URL
(which is what triggers Claude's discovery) and OAuth tokens are accepted. It ships
off because a live claude.ai handshake (web and mobile) has to be verified by a
human before it is enabled. The issuer/base URL is derived from `GoogleRedirectURL`
(the one config value carrying this backend's own scheme+host, as `notify.AckURL`
already does), so no separate public-URL variable is added.

## Consequences

- claude.ai can connect the finance MCP server once the flag is flipped and the
  handshake is verified.
- We now run a (small, single-owner) OAuth authorization server. The blast radius is
  bounded by the admin-only consent, the exact redirect allowlist, PKCE, and the
  per-token audience.
- A future second MCP resource or client would need this generalised (multiple
  clients/resources, maybe dynamic registration). Deferred until there is a second
  consumer.
