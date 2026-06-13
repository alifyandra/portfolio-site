# 1. Go for the backend

Date: 2026-06-13
Status: Accepted

## Context

Alif's production experience is Python (Django/DRF) and Java (Spring). The
backend needs to support future async/queue work and LLM integration, and the
choice was framed primarily around **career ROI over the next ~5 years**, not
just shipping speed.

Candidates considered: FastAPI (Python), Go, Rust.

- **FastAPI** — lowest ramp (already a Python dev), best LLM ecosystem, but a
  lateral move that teaches little new; reads as incremental on a résumé.
- **Rust** — strongest systems signal, but Melbourne market is thin (~6 jobs in
  metro at time of writing) and time-to-productive is months due to the borrow
  checker. Poor ROI for a portfolio project.
- **Go** — new concurrency model (goroutines/channels) and a genuinely new
  paradigm to learn; strong, growing Melbourne demand (~330+ AU roles, hiring at
  Culture Amp, fintech, cloud-adjacent orgs); single-binary containers; aligns
  with Alif's cloud/infra trajectory (AWS cert in progress).

## Decision

Use **Go** for the backend. Mitigate Go's weaker LLM ecosystem by adding a thin
Python (FastAPI/Lambda) sidecar *later*, only when the LLM feature is built —
yielding a polyglot-microservices signal rather than a single-language project.

## Consequences

- Higher learning value than FastAPI; meaningful but not punishing ramp.
- Idiomatic Go (explicit errors, codegen-over-reflection) shapes later choices
  (see ORM and API-contract ADRs).
- LLM work will likely cross a language boundary; accepted as a future cost.

## Alternatives rejected

- FastAPI: too little learned given existing Python depth.
- Rust: market too thin in Melbourne; ramp too costly for the goal.
