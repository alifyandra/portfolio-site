# 7. AWS SQS for the async job queue

Date: 2026-06-13
Status: Accepted

## Context

A future async/LLM feature needs a job queue. The web slice ships first; only
the *wiring* goes in now. Options:

- **asynq** (Redis-backed) — "Celery for Go", reuses the Redis already running
  for the Spotify cache, best local DX, web UI. Self-managed.
- **AWS SQS** — fully managed, scalable, strong cert ROI, clean decoupling.
  Local dev needs LocalStack/ElasticMQ to emulate; separate from Redis.

## Decision

Use **AWS SQS**. Chosen for cert ROI (it's core exam material) and clean
prod decoupling, consistent with the AWS-first direction (ADR 0002).

## Consequences

- Local development emulates SQS via **ElasticMQ** (lightweight, in the compose
  stack) so no AWS account is needed to run locally.
- Producer (API) and consumer (worker) are separate processes from day one,
  even though no real job exists yet — the seam is in place.
- Redis remains for caching only (Spotify), not queueing.

## Alternatives rejected

- asynq: lower friction and reuses Redis, but no managed-service cert value.
- RabbitMQ (Alif used it at OY!): heavier to operate, no cert synergy.
