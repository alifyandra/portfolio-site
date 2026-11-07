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

/** The question asked before accepting: the name the new declaration gets. */
export function acceptPromptText(kind: string): string {
  return `Name for the new recurring ${kind === 'income' ? 'income' : 'bill'}? Its history is linked as ${kind === 'income' ? 'receipts' : 'payments'}.`;
}

/** The confirm step before dismissing: a dismissal is remembered. */
export function dismissPromptText(label: string): string {
  return `Dismiss "${label}"? The same run will not be suggested again.`;
}
