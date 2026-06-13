# 3. Next.js on Vercel, Go backend on AWS

Date: 2026-06-13
Status: Accepted

## Context

The frontend is Next.js + Tailwind. It could be containerised and deployed
alongside the backend (single target) or hosted on Vercel (split target).

## Decision

Host the **frontend on Vercel** (free tier) and the **Go backend on AWS**.
Docker Compose still provides the full local stack; only production is split.

## Consequences

- Vercel handles Next.js optimisations (image optimisation, ISR, edge, preview
  deploys per PR) with zero config and no cost.
- AWS work stays focused on the interesting layer — API, queue, storage.
- Two deploy pipelines (Vercel for frontend, GitHub Actions → EC2 for backend).
- CORS and a configurable API base URL must be handled (frontend and backend on
  different origins).

## Alternatives rejected

- Everything in Docker on AWS: needs `output: 'standalone'`, loses Vercel
  optimisations, and spends effort on frontend infra Vercel gives away free.
