# 2. AWS as the cloud provider

Date: 2026-06-13
Status: Accepted

## Context

Alif has DigitalOcean production experience (Openresim) and an **AWS Solutions
Architect – Associate** cert in progress (expected ~May 2026). The portfolio
needs object storage (S3-style) now and a managed queue later.

## Decision

Deploy on **AWS**. Every hour spent operating the project doubles as hands-on
cert preparation, and the services the project needs (S3, SQS, EC2, IAM) are
core exam material. S3 is native, SQS is the natural future queue, and the
GitHub Actions → AWS deploy flow matches what most employers run.

## Consequences

- More setup/cost complexity than DigitalOcean, accepted for the cert ROI.
- Cost sensitivity drives the compute decision (see ADR 0006): managed,
  cert-grade services (Fargate + ALB + RDS) exceed the stated ~$20 AUD/mo budget.
- Region: ap-southeast-2 (Sydney), closest to Melbourne.

## Alternatives rejected

- DigitalOcean: cheaper and more familiar, but no cert synergy and weaker
  employer signal.
