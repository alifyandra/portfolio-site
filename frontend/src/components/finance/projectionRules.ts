// Pure rules behind the projection overlay on the balance chart (ADR 0019), kept
// free of imports so projectionRules.test.mjs can run them under plain `node --test`.

/** Projection horizons offered beside the history range. 0 turns the overlay off. */
export const PROJECTION_HORIZONS = [
  { days: 0, label: 'No projection' },
  { days: 30, label: 'Project 30d' },
  { days: 90, label: 'Project 90d' },
  { days: 365, label: 'Project 1y' },
] as const;

export interface DatedPoint {
  as_of: string;
  balance: number;
}

/**
 * The projected points that continue a history line: only those strictly after the
 * last history point. The projection opens on today's bucket (the snapshot's own,
 * when that is later), which the history already plots as a reading or a carried
 * close, so drawing it again would put two values on one x. Its close is not lost by
 * dropping it: a balance is a level, so the next projected bucket already carries
 * every flow before it.
 */
export function continuation<T extends DatedPoint>(
  history: readonly DatedPoint[],
  projected: readonly T[],
): T[] {
  let last = -Infinity;
  for (const p of history) {
    const t = Date.parse(p.as_of);
    if (Number.isFinite(t) && t > last) last = t;
  }
  return projected.filter((p) => {
    const t = Date.parse(p.as_of);
    return Number.isFinite(t) && t > last && Number.isFinite(p.balance);
  });
}

/**
 * The projection's bucket step for a history step. The raw reading list has no
 * bucket to continue, so it is continued daily.
 */
export function projectionStep(
  step: string | undefined,
): 'day' | 'week' | 'month' {
  return step === 'week' || step === 'month' ? step : 'day';
}

const REASONS: Record<string, string> = {
  paused: 'paused',
  investment_account: 'on an investment account',
  no_snapshot: 'on an account with no reading',
  currency_mismatch: 'in another currency',
  past_deadline: 'past its deadline',
};

/**
 * One caption sentence naming the declarations the projection left out, so a line
 * that ignores a declared bill says which one and why. Empty when none were.
 */
export function exclusionSummary(
  excluded: readonly { name: string; reason: string }[],
): string {
  if (excluded.length === 0) return '';
  const parts = excluded.map((e) => `${e.name} (${REASONS[e.reason] ?? e.reason})`);
  return `Left out of the projection: ${parts.join(', ')}`;
}
