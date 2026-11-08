// Run with `npm test` (node --test). Every label and date is invented.
import { test } from 'node:test';
import assert from 'node:assert/strict';

import {
  scanSummaryText,
  kindLabel,
  evidenceSpanText,
  evidenceMoreText,
  acceptIntroText,
  acceptPatternHint,
  acceptButtonText,
  dismissPromptText,
} from './suggestionRules.ts';

const zero = {
  rows_scanned: 0,
  runs_found: 0,
  created: 0,
  refreshed: 0,
  declared: 0,
  dismissed: 0,
  reopened: 0,
  dropped: 0,
};

test('a scan summary names only the counts that are not zero', () => {
  assert.equal(
    scanSummaryText({
      ...zero,
      rows_scanned: 812,
      runs_found: 3,
      created: 1,
      declared: 2,
    }),
    'Scanned 812 transactions and found 3 regular runs: 1 new, 2 already declared.',
  );
  assert.equal(
    scanSummaryText({ ...zero, rows_scanned: 1, runs_found: 1, dismissed: 1 }),
    'Scanned 1 transaction and found 1 regular run: 1 dismissed before.',
  );
  assert.equal(
    scanSummaryText({ ...zero, rows_scanned: 40 }),
    'Scanned 40 transactions: no regular runs.',
  );
  assert.equal(
    scanSummaryText({ ...zero, rows_scanned: 40, dropped: 1 }),
    'Scanned 40 transactions: no regular runs, 1 old suggestion removed.',
  );
  assert.equal(
    scanSummaryText({ ...zero, rows_scanned: 9, runs_found: 1, reopened: 1 }),
    'Scanned 9 transactions and found 1 regular run: 1 reopened after its declaration ended.',
  );
});

test('the kind says what accepting creates', () => {
  assert.equal(kindLabel('bill'), 'Bill');
  assert.equal(kindLabel('income'), 'Income');
});

test('the evidence span counts every row, not just the ones listed', () => {
  const fmt = (d) => `[${d}]`;
  assert.equal(
    evidenceSpanText(
      'bill',
      { count: 8, since: '2026-09-05', newest: '2026-11-05', gaps: 0 },
      fmt,
    ),
    '8 debits, [2026-09-05] to [2026-11-05]',
  );
  assert.equal(
    evidenceSpanText(
      'income',
      { count: 1, since: '2026-11-05', newest: '2026-11-05', gaps: 0 },
      fmt,
    ),
    '1 credit, [2026-11-05]',
  );
  assert.equal(
    evidenceSpanText(
      'bill',
      { count: 5, since: '2026-06-03', newest: '2026-11-03', gaps: 1 },
      fmt,
    ),
    '5 debits, 1 skipped cycle, [2026-06-03] to [2026-11-03]',
  );
  assert.equal(
    evidenceSpanText(
      'income',
      { count: 0, since: '', newest: '', gaps: 0 },
      fmt,
    ),
    '0 credits',
  );
});

test('a trimmed evidence list says how much more there is', () => {
  assert.equal(evidenceMoreText(5, 8), 'Newest 5 of 8. Show all');
  assert.equal(evidenceMoreText(4, 4), '');
});

test('accept says what it creates and dismiss asks first', () => {
  assert.equal(
    acceptIntroText('income'),
    'Creates a recurring income and links this history as its receipts.',
  );
  assert.equal(
    acceptIntroText('bill'),
    'Creates a recurring bill and links this history as its payments.',
  );
  assert.equal(
    dismissPromptText('ZYLO STREAMING'),
    'Dismiss "ZYLO STREAMING"? The same run will not be suggested again.',
  );
});

test('the accept form shows the suggested pattern as a suggestion the owner confirms', () => {
  assert.equal(
    acceptPatternHint('bill', 'ZYLO STREAMING', 'ZYLO STREAMING'),
    'Suggested from the evidence. Payments containing this text will link automatically; edit it, or clear it to link each one by hand.',
  );
  assert.equal(
    acceptPatternHint('income', 'Salary ACME', '  acme  '),
    'Your pattern. Receipts containing this text will link automatically.',
  );
  assert.equal(
    acceptPatternHint('income', 'Salary ACME', '  '),
    'No pattern: each receipt is linked by hand.',
  );
  assert.equal(
    acceptPatternHint('bill', '', ''),
    'Nothing in the evidence to match on; type a pattern, or leave it empty to link each payment by hand.',
  );
  assert.equal(
    acceptButtonText('bill', 'ZYLO'),
    'Create bill with this pattern',
  );
  assert.equal(acceptButtonText('income', ' '), 'Create income, link by hand');
});
