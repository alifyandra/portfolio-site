// Pure rules behind the recurring-income section, kept free of imports so
// incomeRules.test.mjs can run them under plain `node --test`.

const melbourneDay = new Intl.DateTimeFormat('en-AU', {
  timeZone: 'Australia/Melbourne',
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
});

/**
 * Today as YYYY-MM-DD on the Melbourne calendar, the clock the backend measures pay
 * dates against (localToday in bills.go). toISOString() is the UTC day, which is still
 * yesterday for the first ten or eleven hours of every Melbourne morning.
 */
export function melbourneTodayISO(now: Date = new Date()): string {
  const parts = Object.fromEntries(
    melbourneDay.formatToParts(now).map((p) => [p.type, p.value]),
  );
  return `${parts.year}-${parts.month}-${parts.day}`;
}

/**
 * The status the Pause/Resume button moves an income to, or null when it has no such
 * button: an ended income is finished, and pausing it would quietly un-end it.
 */
export function pauseToggleTarget(status: string): 'active' | 'paused' | null {
  if (status === 'ended') return null;
  return status === 'paused' ? 'active' : 'paused';
}

/** Whether the End action applies: anything not already ended. */
export function canEnd(status: string): boolean {
  return status !== 'ended';
}

/**
 * The PATCH body that ends an income. The API requires status and ended_on together,
 * so both always go in one request.
 */
export function endPatch(endedOn: string) {
  return { status: 'ended' as const, ended_on: endedOn };
}

/** "in 4 days" / "today", from days_until (never negative). */
function whenLabel(days: number): string {
  if (days === 0) return 'today';
  return `in ${days} day${days === 1 ? '' : 's'}`;
}

/**
 * The row's schedule line. next_expected is null once no pay date is left, in which case
 * the end date says everything; otherwise the next pay date leads and ended_on, when
 * set, follows it.
 */
export function scheduleText(
  i: {
    next_expected: string | null;
    days_until: number | null;
    ended_on: string | null;
  },
  fmt: (d: string) => string,
): string {
  if (i.next_expected == null || i.days_until == null) {
    return i.ended_on ? `Ended ${fmt(i.ended_on)}` : 'No pay date ahead';
  }
  const next = `Next ${fmt(i.next_expected)} · ${whenLabel(i.days_until)}`;
  return i.ended_on ? `${next} · ends ${fmt(i.ended_on)}` : next;
}

/** Whole days from a to b, both YYYY-MM-DD calendar dates (negative when b is earlier). */
function daysFromTo(a: string, b: string): number {
  return Math.round(
    (Date.parse(`${b}T00:00:00Z`) - Date.parse(`${a}T00:00:00Z`)) / 86_400_000,
  );
}

/**
 * How a receipt's credit landed against the pay date it settles: "on the day", "2 days
 * early" (pay due on a weekend usually posts the Friday before), or "3 days late".
 */
export function paidTimingLabel(
  occurrenceDate: string,
  postedDate: string,
): string {
  const early = daysFromTo(postedDate, occurrenceDate);
  if (early === 0) return 'on the day';
  const n = Math.abs(early);
  return `${n} day${n === 1 ? '' : 's'} ${early > 0 ? 'early' : 'late'}`;
}

/**
 * The confirm step before a receipt is removed. Only the link goes: the transaction is
 * ledger data and stays, so the cycle can be linked again afterwards.
 */
export function removeReceiptPrompt(
  incomeName: string,
  occurrenceDate: string,
  fmt: (d: string) => string,
): string {
  return `Remove the receipt for "${incomeName}" on ${fmt(occurrenceDate)}? The transaction stays in the ledger; only the link goes.`;
}
