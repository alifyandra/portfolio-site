// Run with `npm test` (node --test). Node strips the types from projectionRules.ts
// itself, so these need no test framework. All dates and amounts are invented.
import { test } from 'node:test';
import assert from 'node:assert/strict';

import {
  continuation,
  projectionStep,
  exclusionSummary,
} from './projectionRules.ts';

const history = [
  { as_of: '2026-11-07T00:00:00+11:00', balance: 900 },
  { as_of: '2026-11-08T00:00:00+11:00', balance: 1000 },
];

test('a projection continues after the last history bucket, never on it', () => {
  const projected = [
    { as_of: '2026-11-08T00:00:00+11:00', balance: 1000, projected: true },
    { as_of: '2026-11-09T00:00:00+11:00', balance: 950, projected: true },
    { as_of: '2026-11-10T00:00:00+11:00', balance: 950, projected: true },
  ];
  assert.deepEqual(
    continuation(history, projected).map((p) => p.as_of),
    ['2026-11-09T00:00:00+11:00', '2026-11-10T00:00:00+11:00'],
  );
});

test('the comparison is by instant, not by string', () => {
  // A raw reading at 09:00 local is rendered in UTC; the day bucket it falls in
  // starts earlier, so only the next day continues it.
  const raw = [{ as_of: '2026-11-07T22:00:00Z', balance: 1000 }];
  const projected = [
    { as_of: '2026-11-08T00:00:00+11:00', balance: 1000 },
    { as_of: '2026-11-09T00:00:00+11:00', balance: 990 },
  ];
  assert.deepEqual(
    continuation(raw, projected).map((p) => p.balance),
    [990],
  );
});

test('with no history every plottable projected point is kept', () => {
  const projected = [
    { as_of: '2026-11-08T00:00:00+11:00', balance: 1000 },
    { as_of: 'not a date', balance: 1 },
    { as_of: '2026-11-09T00:00:00+11:00', balance: Number.NaN },
  ];
  assert.equal(continuation([], projected).length, 1);
});

test('raw history is continued daily, bucketed history on its own step', () => {
  assert.equal(projectionStep(undefined), 'day');
  assert.equal(projectionStep('day'), 'day');
  assert.equal(projectionStep('week'), 'week');
  assert.equal(projectionStep('month'), 'month');
});

test('left-out declarations are named with a readable reason', () => {
  assert.equal(exclusionSummary([]), '');
  assert.equal(
    exclusionSummary([
      { name: 'Gym', reason: 'paused' },
      { name: 'Dividend', reason: 'investment_account' },
      { name: 'Glasses', reason: 'past_deadline' },
    ]),
    'Left out of the projection: Gym (paused), Dividend (on an investment account), Glasses (past its deadline)',
  );
});
