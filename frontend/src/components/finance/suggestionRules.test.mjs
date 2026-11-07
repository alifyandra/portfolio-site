// Run with `npm test` (node --test). Every label and date is invented.
import { test } from 'node:test';
import assert from 'node:assert/strict';

import {
  scanSummaryText,
  kindLabel,
  evidenceSpanText,
  acceptPromptText,
  dismissPromptText,
} from './suggestionRules.ts';

const zero = {
  rows_scanned: 0,
  runs_found: 0,
  created: 0,
  refreshed: 0,
  declared: 0,
  dismissed: 0,
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
});

test('the kind says what accepting creates', () => {
  assert.equal(kindLabel('bill'), 'Bill');
  assert.equal(kindLabel('income'), 'Income');
});

test('the evidence span runs oldest to newest though the rows arrive newest first', () => {
  const fmt = (d) => `[${d}]`;
  const rows = [
    { posted_date: '2026-11-05' },
    { posted_date: '2026-10-05' },
    { posted_date: '2026-09-05' },
  ];
  assert.equal(
    evidenceSpanText('bill', rows, fmt),
    '3 debits, [2026-09-05] to [2026-11-05]',
  );
  assert.equal(
    evidenceSpanText('income', rows.slice(0, 1), fmt),
    '1 credit, [2026-11-05]',
  );
  assert.equal(evidenceSpanText('income', [], fmt), '0 credits');
});

test('accept and dismiss both ask first, in the words of what happens', () => {
  assert.equal(
    acceptPromptText('income'),
    'Name for the new recurring income? Its history is linked as receipts.',
  );
  assert.equal(
    acceptPromptText('bill'),
    'Name for the new recurring bill? Its history is linked as payments.',
  );
  assert.equal(
    dismissPromptText('ZYLO STREAMING'),
    'Dismiss "ZYLO STREAMING"? The same run will not be suggested again.',
  );
});
