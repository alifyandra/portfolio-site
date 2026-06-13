# 4. Ent as the ORM (over GORM and sqlc)

Date: 2026-06-13
Status: Accepted

## Context

Alif prefers "heavy magic" ORMs (Django ORM, Spring/JPA) and dislikes writing
raw SQL, but also wants to learn something genuinely new in Go. Options:

- **sqlc** — SQL-first, generates typed Go from `.sql`. High learning value and
  plays to Alif's Postgres-optimisation strength, but SQL-first is explicitly
  not Alif's happy place.
- **GORM** — Django-like, familiar, but reflection-based (runtime magic), the
  least idiomatic Go option, and known to "fight you" at scale.
- **Ent** (Meta) — schema-as-Go-code with **code generation** (idiomatic Go's
  approach to magic), fully type-safe, excellent at relations, strong résumé
  keyword. No raw SQL for everyday use.

## Decision

Use **Ent**. It preserves the ORM ergonomics Alif likes while staying idiomatic
(codegen, not reflection), so it still teaches Go's codegen-first philosophy —
the same philosophy reused for the API contract (ADR 0005).

## Consequences

- Must learn Ent's schema DSL and run codegen (`go generate ./ent`).
- Smaller community than GORM; mitigated by good official docs.
- `entoas` (OpenAPI-from-Ent) was **deliberately not used** — it couples the
  public API shape 1:1 to DB tables. The API contract stays independent.

## Alternatives rejected

- GORM: reflection-based, weaker long-term, less learned.
- sqlc: SQL-first conflicts with stated preference; would risk momentum.
