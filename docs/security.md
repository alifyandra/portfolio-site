# Security Runbook

How this site is hardened, and what to do as it grows (Cloudflare, the contact
form, and auth). Pairs with [deployment.md](deployment.md).

## Threat model (what we actually care about)

A personal portfolio. Realistic risks, in order:

1. **Contact-form abuse** — spam submissions → DB bloat + SES cost/inbox flooding.
2. **Opportunistic DDoS / L7 floods** — a single small EC2 box has no inherent
   resilience.
3. **Backend compromise once auth exists** — credential stuffing, token theft,
   privilege escalation.

We are *not* defending against a determined, resourced attacker — the goal is to
raise the bar well above drive-by/automated threats.

## Current posture (shipped)

| Control | Where |
|---|---|
| TLS everywhere | Caddy auto-HTTPS (origin) + Cloudflare edge (once added) |
| Parameterised queries (no SQLi) | Ent |
| Output escaping (no stored XSS) | React default escaping |
| Edge input validation | Huma (types, lengths, email format) |
| CORS locked to the frontend origin | `internal/server` |
| No static AWS keys on the box | EC2 IAM instance role |
| No public write endpoints | `POST /api/projects` removed; projects are seed-only |
| Contact honeypot (silent drop of bots) | hidden `website` field + handler |
| Per-IP rate limit (100/min) | `go-chi/httprate` middleware |
| Security headers | Caddyfile (API) + `next.config.mjs` (frontend) |
| Dependency vuln scan | `govulncheck` in CI |
| Automated dependency updates | Dependabot (go / npm / actions) |

## Backend exposure note (important)

The frontend calls the Go API **directly from the browser** (`NEXT_PUBLIC_API_URL`
is in the client bundle). Routing calls through Vercel (rewrites / server
components) would hide the URL, but **does not protect the box** — Vercel's
egress IPs aren't stable/allowlistable, so the origin stays directly reachable.
Real protection comes from putting Cloudflare in front and firewalling the origin
to Cloudflare's IP ranges (below).

## Cloudflare (the DDoS / WAF layer — free plan)

```
Visitor ──▶ Cloudflare edge (DDoS, WAF, cache, challenges) ──▶ EC2 origin
            (domain DNS points here)                          (SG: Cloudflare IPs only)
```

> **Now codified.** The proxy + origin lock are Terraform flags (`proxy_api`,
> `lock_origin_to_cloudflare`, both default off) with Caddy switched to a
> Cloudflare origin certificate so it no longer needs ACME once locked. See the
> step-by-step cutover in
> [`deploy/terraform/README.md`](../deploy/terraform/README.md#cloudflare-proxy-cutover-origin-lock).
> The manual narrative below explains the *why*.

### Setup order (do the SG lock LAST so you don't lock yourself out)

1. **Deploy + verify the origin first.** Bring up the backend on EC2 with the
   domain and Caddy TLS working *directly* — confirm `https://api.<domain>/healthz`
   returns 200 before involving Cloudflare.
2. **Add the domain to Cloudflare** (free plan). It scans existing DNS and gives
   you two Cloudflare nameservers.
3. **Point nameservers** at Cloudflare (at your registrar). Propagation: minutes
   to a few hours.
4. **DNS records — Proxied (orange cloud):**
   - `api.<domain>` → A → EC2 public IP → **Proxied**
   - Frontend stays on Vercel (use Vercel's domain config, or a proxied CNAME).
5. **SSL/TLS mode → "Full (strict)".** Browser→CF and CF→origin both HTTPS, and
   CF validates the origin cert. Caddy already serves a valid Let's Encrypt cert,
   so this just works. **Never use "Flexible"** (leaves CF→origin unencrypted).
6. **Lock the origin to Cloudflare.** EC2 security group inbound: allow 443 (and
   80 for ACME) **only from** Cloudflare's published ranges
   (https://www.cloudflare.com/ips/), not `0.0.0.0/0`. Now the box is unreachable
   except via Cloudflare. (Manage the box via **SSM Session Manager**, so no
   public SSH port is needed at all.)
7. **Enable the freebies:**
   - **Rate-limiting rule** on `POST /api/contact` (e.g. 5/min/IP) — on top of the
     app-layer 100/min.
   - **Bot Fight Mode** + baseline **WAF managed rules**.
   - **Cache** static frontend assets / chosen GET responses.
   - **Under Attack Mode** — one-click JS-challenge-everyone toggle for active
     attacks only (hurts UX; leave off normally).

### Code tweak when Cloudflare is live

Behind Cloudflare the real client IP arrives in `CF-Connecting-IP`. **Done:** when
`TRUST_CLOUDFLARE_IP=true` the rate limiter keys off that header
(`keyByCloudflareIP` in `backend/internal/server/server.go`), falling back to the
connecting IP when the header is absent or not a valid IP, so limits apply per
real visitor rather than per Cloudflare edge node. The header is only trustworthy
once the SG is locked to CF (otherwise it can be spoofed by a direct request), so
the flag rides on `lock_origin_to_cloudflare` — Terraform sets `TRUST_CLOUDFLARE_IP`
to match, keeping the spoofable-header path inert until the origin lock is live.

## Contact form: CAPTCHA with Turnstile

The honeypot + rate limit stop most spam. If abuse persists, add **Cloudflare
Turnstile** (free reCAPTCHA alternative):

1. Create a Turnstile widget in the Cloudflare dashboard → get a site key + secret.
2. Render the widget in the contact form; it produces a token on submit.
3. Send the token with the form; the backend verifies it server-side against
   `https://challenges.cloudflare.com/turnstile/v0/siteverify` before saving.

Prefer Turnstile on the form over challenging all traffic — it gates the abuse
target without hurting general UX.

## Domain registrar

- **Cloudflare Registrar**: at-cost (no markup) + free WHOIS privacy, auto-uses
  CF DNS. Best for generic TLDs (`.com`, `.dev`, `.io`).
- **`.au` / `.com.au`**: not supported by Cloudflare Registrar — register at an
  Australian registrar (VentraIP, Synergy Wholesale, …) and point nameservers at
  Cloudflare. (Good local signal for a Melbourne job hunt.)
- You never need to buy from Cloudflare to *use* Cloudflare — any registrar works
  via nameserver change.

## Auth hardening (when auth is added — see scope; deferred for v1)

| Area | Do |
|---|---|
| Don't roll your own | **AWS Cognito** (managed + cert-relevant) or a vetted Go JWT lib |
| Password storage | argon2id (or bcrypt) — never plaintext / fast hashes |
| Tokens | Short-lived access + refresh; **HttpOnly Secure cookies**, not localStorage; CSRF protection if cookie-based |
| Brute force | Per-IP login rate limit + backoff; avoid account enumeration |
| 2FA | TOTP |
| AuthZ ≠ AuthN | Role checks on every write/admin route, not just "logged in" |
| Re-gate writes | Restore `POST /api/projects` etc. behind an auth requirement |
| Secrets | Move `.env` → **AWS Secrets Manager / SSM Parameter Store** |
| Transport | HTTPS only (Caddy ✓), HSTS (✓) |

## Identifiers in committed files (public repo)

This repo is public and real financial data flows through the finance code, so every
committed file is published content. That includes test fixtures and doc comments, not
just prose. A fixture is the easiest place for a real value to land unnoticed, because
it looks like plumbing and gets skimmed in review.

### The mechanical half: reserved masked numbers

`scripts/check-fixtures.sh` runs in CI (`make check-fixtures` locally) and fails if a
masked account number outside a reserved set appears in any committed file.

It is an **allowlist, not a denylist**. A denylist would have to name the real values,
and the check runs in public CI, so it would publish the thing it protects. Inverted,
the check only needs to know which fakes are sanctioned:

| Style | Values |
|---|---|
| Repeated digit | `0000` `1111` `2222` `3333` `4444` `5555` `6666` `7777` `8888` `9999` |
| Repeated pair | `4242` `4343` `5353` `6464` |
| Sequential (wire payloads) | `1234` |

Any of the masked forms is matched: `xxxx 4242`, `xxxx xxxx 1234`, `****1234`, and the
inline `xx5353` that CommBank transfer descriptions use. Bare four-digit numbers are out
of scope, because they are ubiquitous and carry no account semantics on their own.

Need a value the set does not have? Add an obviously-synthetic one to the list in the
same commit. That diff line is deliberate friction. It is the moment a reviewer asks
whether the number was invented or copied.

### The half a regex cannot check

These stay a review concern, because a regex that caught them would either miss
constantly or leak the patterns it matches on:

- employer, merchant and payee names
- exact row counts, coverage percentages, precise date spans
- anything transcribed from a real statement rather than made up

**The pairing rule:** a name that is fine alone is not fine sitting next to finance
figures. The site's own résumé data names an employer deliberately and that is correct;
the same name beside a salary-shaped amount is a leak. It is the combination that
discloses, not either half.

This applies to issues, PR titles and bodies, and commit messages as much as to code.
Note that **`gh issue edit` does not redact**. GitHub keeps prior bodies in
`userContentEdits`, publicly readable via GraphQL, so delete and re-file instead.

## Ongoing hygiene

- Review **Dependabot** PRs; let CI gate them.
- `govulncheck` runs in CI — treat failures as release blockers.
- Rotate any credentials that ever touch a non-instance-role path.
- Keep the résumé / PII **out of git** (`*.pdf` is gitignored; history was scrubbed).
- Keep real identifiers out of committed files (see the section above). CI enforces the
  mechanical half.
