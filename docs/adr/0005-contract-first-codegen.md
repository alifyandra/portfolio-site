# 5. Code-first OpenAPI with Huma + orval

Date: 2026-06-13
Status: Accepted

## Context

Alif runs a contract-driven Django + Next workflow at work (drf-spectacular →
OpenAPI → generated TS types) and wants the same type-safe sync here. Two
philosophies:

- **Code-first** — write Go, derive the spec. Mirrors drf-spectacular muscle
  memory.
- **Spec-first** — hand-write `openapi.yaml`, generate server + client. More
  disciplined but inverts the familiar workflow.

## Decision

**Code-first with Huma** on top of Chi: Go handler types derive the OpenAPI
spec automatically. The frontend uses **orval** to generate typed **React
Query/SWR hooks** from that spec. Pipeline: edit Go handler → regenerate spec →
frontend codegen → typed hooks.

A **CI check fails the build if the committed spec is stale**, so the contract
can never silently drift.

## Consequences

- Huma sits over Chi (Chi remains the router); one more layer to understand.
- Spec is a generated artifact committed to the repo and consumed by the
  frontend build.
- Reuses the codegen philosophy already chosen for the ORM (ADR 0004).

## Alternatives rejected

- Spec-first (oapi-codegen): solid and contract-pure, but inverts Alif's proven
  workflow.
- swaggo/swag: annotation-comment driven, less type-safe, older.
