# 1. Go for the backend

Date: 2026-06-13
Status: Accepted

## Context

The backend has to support async/queue work now and an LLM integration later,
and it needs to run cheaply as a single container on a small box (see the EC2
and compose ADR). Since this is also a project to learn on, a language with a
concurrency model worth getting fluent in counted in its favour.

Candidates considered: FastAPI (Python), Go, Rust.

- FastAPI: quickest to build and the strongest LLM ecosystem, but mostly
  familiar territory, so there isn't much new to pick up.
- Rust: the strongest systems story, but the ramp (the borrow checker, a longer
  time to productive) is hard to justify for a project this size.
- Go: a different concurrency model (goroutines and channels), compiles to one
  static binary that drops cleanly into a minimal container, and has a healthy
  cloud-native ecosystem.

## Decision

Use Go for the backend. Its thinner LLM ecosystem is an acceptable trade: when
the LLM feature actually gets built, add a small Python sidecar (FastAPI or a
Lambda) for that part instead of forcing it into the Go service.

## Consequences

- Single-binary builds keep the image small and the deploy simple.
- Idiomatic Go (explicit errors, codegen over reflection) shapes later choices;
  see the ORM and API-contract ADRs.
- The eventual LLM work will cross a language boundary. That is accepted as a
  future cost.

## Alternatives rejected

- FastAPI: familiar territory, so too little new to learn here.
- Rust: the ramp costs more than it is worth for this project.
