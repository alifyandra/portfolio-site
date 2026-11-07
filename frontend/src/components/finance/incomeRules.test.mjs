// Run with `npm test` (node --test). Node strips the types from incomeRules.ts itself,
// so these need no test framework. All dates are invented.
import { test } from 'node:test';
import assert from 'node:assert/strict';

import {
  melbourneTodayISO,
  pauseToggleTarget,
  canEnd,
  endPatch,
  scheduleText,
  paidTimingLabel,
  removeReceiptPrompt,
} from './incomeRules.ts';

test('today is the Melbourne calendar day, not the UTC one', () => {
  // 07:00 AEDT on 8 Nov is still 7 Nov in UTC: the anchor must default to the 8th.
  assert.equal(
    melbourneTodayISO(new Date('2026-11-07T20:00:00Z')),
    '2026-11-08',
  );
  // 01:30 AEST on 1 Jul (winter, +10).
  assert.equal(
    melbourneTodayISO(new Date('2026-06-30T15:30:00Z')),
    '2026-07-01',
  );
  // 23:59 local stays on the same day.
  assert.equal(
    melbourneTodayISO(new Date('2026-11-08T12:59:00Z')),
    '2026-11-08',
  );
});

test('pause and resume never apply to an ended income', () => {
  assert.equal(pauseToggleTarget('active'), 'paused');
  assert.equal(pauseToggleTarget('paused'), 'active');
  assert.equal(pauseToggleTarget('ended'), null);
});

test('only a running income can be ended, and ending sends both fields', () => {
  assert.equal(canEnd('active'), true);
  assert.equal(canEnd('paused'), true);
  assert.equal(canEnd('ended'), false);
  assert.deepEqual(endPatch('2026-11-08'), {
    status: 'ended',
    ended_on: '2026-11-08',
  });
});

test('the row names the next pay date, and ended_on once set', () => {
  const id = (d) => d;
  assert.equal(
    scheduleText(
      { next_expected: '2026-11-09', days_until: 1, ended_on: null },
      id,
    ),
    'Next 2026-11-09 · in 1 day',
  );
  assert.equal(
    scheduleText(
      { next_expected: '2026-11-08', days_until: 0, ended_on: '2026-11-20' },
      id,
    ),
    'Next 2026-11-08 · today · ends 2026-11-20',
  );
  // No pay date left: the end date is the whole story, never a past "next".
  assert.equal(
    scheduleText(
      { next_expected: null, days_until: null, ended_on: '2026-10-29' },
      id,
    ),
    'Ended 2026-10-29',
  );
  assert.equal(
    scheduleText({ next_expected: null, days_until: null, ended_on: null }, id),
    'No pay date ahead',
  );
});

test('a receipt says how its credit landed against the pay date', () => {
  // A Sunday pay date paid on the Friday.
  assert.equal(paidTimingLabel('2026-11-15', '2026-11-13'), '2 days early');
  assert.equal(paidTimingLabel('2026-11-15', '2026-11-14'), '1 day early');
  assert.equal(paidTimingLabel('2026-11-15', '2026-11-15'), 'on the day');
  assert.equal(paidTimingLabel('2026-11-15', '2026-11-18'), '3 days late');
  // Across a month end and a daylight-saving change.
  assert.equal(paidTimingLabel('2026-10-04', '2026-09-30'), '4 days early');
});

test('removing a receipt asks first and says the transaction stays', () => {
  const text = removeReceiptPrompt('Salary', '2026-11-15', (d) => `[${d}]`);
  assert.equal(
    text,
    'Remove the receipt for "Salary" on [2026-11-15]? The transaction stays in the ledger; only the link goes.',
  );
});
