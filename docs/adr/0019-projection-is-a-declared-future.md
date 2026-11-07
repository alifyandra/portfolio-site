# ADR 0019: The projection is a declared future, not a trend

Status: Accepted
Date: 2026-11-07
Relates to: ADR 0017 (finance read dashboard), the #122 principle that owner-authored
fields are never inferred or overwritten.

## Context

The finance dashboard shows where the balances have been (the Balance Series, ADR 17)
and nothing about where they are going. The obvious projection is a trend line: fit
the last N buckets of net worth and extend it. It needs no new data and reads well on
a chart.

It also lies in exactly the cases the owner cares about. A one-off inside the window
(a car service, a tax refund, a laptop) bends the line for months, and the line can
never say *why* it goes where it goes. The owner already declares the future in two
entities that carry a cadence and an anchor date: Recurring Bills, and (new in this
decision) Recurring Income.

## Decision

A **Projection** is a forward Balance Series under one stated premise, *nothing
changes*: start from each account's latest Balance Snapshot and walk forward applying
only what the owner has declared, every Recurring Bill and Recurring Income
occurrence in the horizon, and optionally each Wishlist Item that has both a cost and
a deadline. Investment accounts are carried flat. Net worth is the sum. Every
projected point is marked `projected` so it can never be read as a reading.

**Recurring Income** is a sibling entity of Recurring Bill with the same shape
(cadence, anchor date, expected amount, optional account) and the opposite direction,
and its own occurrence link to the Posted Transaction that paid it. It is not a
signed bill: a bill is a commitment and income is not, and the glossary's avoid-list
on "bill" shows the word is meant precisely.

A **Recurrence Suggestion** pass bootstraps the declarations from history
(near-constant counterparty, amount and interval) and only ever *suggests*; the owner
accepts or dismisses each one, and a dismissal is remembered. The pass never creates
a declaration, for the same reason the ingest never writes an account's description:
declarations are owner-authored. The categorisation work in #89 stays separate; the
projection does not depend on it.

## Considered options

- **Trend fit** (regression over recent buckets): rejected as the primary line for
  the reasons above. It may return later as a second, clearly labelled line.
- **Signed Recurring Bill** instead of a new entity: rejected, it makes "bill" mean
  "any recurring flow".
- **Infer income from recurring credits** without a declaration: rejected, it is
  silent when a pay cycle shifts and it inverts the owner-authored rule.
- **Fold recurrence detection into the #89 LLM batch:** rejected, recurrence is
  arithmetic on dates and amounts, not a language problem, and it would tie the
  projection to an issue blocked on a scoping call.

## Consequences

- The projection is only as complete as what has been declared. On day one, with no
  bills or income typed, it is a flat line, which is the honest answer.
- Build order follows from the dependencies: Recurring Income, then the suggestion
  pass, then the projection. The wishlist stays hand-typed; a backread cannot know
  what the owner wants.
- Every projected point carries a marker the same way ledger points carry a `source`,
  and the chart must render it distinctly from history.
