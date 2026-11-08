// Pure rules behind the recurrence suggestions section, kept free of imports so
// suggestionRules.test.mjs can run them under plain `node --test`.

interface ScanSummary {
  rows_scanned: number;
  runs_found: number;
  created: number;
  refreshed: number;
  declared: number;
  dismissed: number;
  reopened: number;
  dropped: number;
}

/** One line saying what a scan found, naming only the counts that are not zero. */
export function scanSummaryText(s: ScanSummary): string {
  const scanned = `Scanned ${s.rows_scanned} transaction${s.rows_scanned === 1 ? '' : 's'}`;
  if (s.runs_found === 0) {
    return s.dropped > 0
      ? `${scanned}: no regular runs, ${s.dropped} old suggestion${s.dropped === 1 ? '' : 's'} removed.`
      : `${scanned}: no regular runs.`;
  }
  const parts = [
    [s.created, 'new'],
    [s.refreshed, 'updated'],
    [s.declared, 'already declared'],
    [s.dismissed, 'dismissed before'],
    [s.reopened, 'reopened after its declaration ended'],
    [s.dropped, 'removed'],
  ]
    .filter(([n]) => (n as number) > 0)
    .map(([n, what]) => `${n} ${what}`);
  return `${scanned} and found ${s.runs_found} regular run${s.runs_found === 1 ? '' : 's'}: ${parts.join(', ')}.`;
}

/** "Bill" or "Income": what accepting creates. */
export function kindLabel(kind: string): string {
  return kind === 'income' ? 'Income' : 'Bill';
}

/** What the evidence span line needs: the run's size, oldest and newest dates, skips. */
interface EvidenceSpan {
  count: number;
  since: string;
  newest: string;
  gaps: number;
}

/**
 * The evidence span, e.g. "5 debits, 1 skipped cycle, 3 Jun 2026 to 3 Nov 2026". It is
 * built from the run's count and dates, not from the listed rows, since the list carries
 * only the newest few.
 */
export function evidenceSpanText(
  kind: string,
  span: EvidenceSpan,
  fmt: (d: string) => string,
): string {
  const n = span.count;
  const parts = [
    `${n} ${kind === 'income' ? 'credit' : 'debit'}${n === 1 ? '' : 's'}`,
  ];
  if (span.gaps > 0) {
    parts.push(`${span.gaps} skipped cycle${span.gaps === 1 ? '' : 's'}`);
  }
  if (n > 0) {
    parts.push(
      span.since === span.newest
        ? fmt(span.newest)
        : `${fmt(span.since)} to ${fmt(span.newest)}`,
    );
  }
  return parts.join(', ');
}

/** The note under a trimmed evidence list; empty when every row is already shown. */
export function evidenceMoreText(shown: number, count: number): string {
  return shown < count ? `Newest ${shown} of ${count}. Show all` : '';
}

/** The line above the accept form: what accepting creates and links. */
export function acceptIntroText(kind: string): string {
  const income = kind === 'income';
  return `Creates a recurring ${income ? 'income' : 'bill'} and links this history as its ${income ? 'receipts' : 'payments'}.`;
}

/**
 * The hint under the accept form's match pattern. The pattern is the owner's: the
 * suggested one is only a starting value, so the hint says which it is and what it does.
 */
export function acceptPatternHint(
  kind: string,
  suggested: string,
  current: string,
): string {
  const rows = kind === 'income' ? 'Receipts' : 'Payments';
  const one = kind === 'income' ? 'receipt' : 'payment';
  const pattern = current.trim();
  if (pattern === '') {
    return suggested.trim() === ''
      ? `Nothing in the evidence to match on; type a pattern, or leave it empty to link each ${one} by hand.`
      : `No pattern: each ${one} is linked by hand.`;
  }
  if (pattern === suggested.trim()) {
    return `Suggested from the evidence. ${rows} containing this text will link automatically; edit it, or clear it to link each one by hand.`;
  }
  return `Your pattern. ${rows} containing this text will link automatically.`;
}

/** The accept form's confirm button, naming what the pattern will do. */
export function acceptButtonText(kind: string, pattern: string): string {
  const what = kind === 'income' ? 'income' : 'bill';
  return pattern.trim() === ''
    ? `Create ${what}, link by hand`
    : `Create ${what} with this pattern`;
}

/** The confirm step before dismissing: a dismissal is remembered. */
export function dismissPromptText(label: string): string {
  return `Dismiss "${label}"? The same run will not be suggested again.`;
}
