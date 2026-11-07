// Pure rules behind the recurrence suggestions section, kept free of imports so
// suggestionRules.test.mjs can run them under plain `node --test`.

interface ScanSummary {
  rows_scanned: number;
  runs_found: number;
  created: number;
  refreshed: number;
  declared: number;
  dismissed: number;
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

/**
 * The evidence span, e.g. "4 debits, 5 Aug 2026 to 5 Nov 2026". Evidence arrives newest
 * first, so the oldest row is the last one.
 */
export function evidenceSpanText(
  kind: string,
  evidence: { posted_date: string }[],
  fmt: (d: string) => string,
): string {
  const n = evidence.length;
  const rows = `${n} ${kind === 'income' ? 'credit' : 'debit'}${n === 1 ? '' : 's'}`;
  if (n === 0) return rows;
  const newest = evidence[0].posted_date;
  const oldest = evidence[n - 1].posted_date;
  return newest === oldest
    ? `${rows}, ${fmt(newest)}`
    : `${rows}, ${fmt(oldest)} to ${fmt(newest)}`;
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
