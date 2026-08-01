'use client';

// Balance-over-time: an account selector (defaulting to the first asset account)
// + a basis + a look-back range + a bucket step, feeding the hand-rolled
// BalanceChart. The accounts query is shared with AccountsSection (React Query
// dedupes by key), so this adds no extra round-trip for the account list.
//
// Each range carries a default step, because a 1y view of raw readings plots
// roughly 365 near-identical points into a few hundred pixels: wasted work and a
// noisier line than the trend deserves. The step select overrides it, and "Raw"
// asks for the unbucketed per-reading series.
//
// Basis picks where the series comes from. Snapshot (the default, so nothing
// about the existing view changes) reads the bank's balance readings. Ledger
// derives close, open and per-period in/out from posted transactions, which
// also fills in the series-level flags rendered as a caption below the chart.

import { useState } from 'react';

import {
  useListFinanceAccounts,
  useGetFinanceBalanceHistory,
} from '@/lib/api/generated';
import { citronCard, citronBadge, selectClass } from '@/components/admin/ui';
import { BalanceChart } from './BalanceChart';
import { formatMoney, formatDate } from './format';

// Default step per look-back window: fine over a short window, coarse over a long
// one. Buckets align to Australia/Melbourne local boundaries server-side.
const RANGES = [
  { label: '30d', days: 30, step: 'day' },
  { label: '90d', days: 90, step: 'day' },
  { label: '1y', days: 365, step: 'week' },
  { label: 'All', days: 0, step: 'month' },
] as const;

// '' is Auto (take the range's default). 'raw' asks for no step at all.
const STEP_OPTIONS = [
  { value: '', label: 'Auto step' },
  { value: 'day', label: 'Daily' },
  { value: 'week', label: 'Weekly' },
  { value: 'month', label: 'Monthly' },
  { value: 'raw', label: 'Raw readings' },
] as const;

const BASES = [
  { value: 'snapshot', label: 'Snapshot' },
  { value: 'ledger', label: 'Ledger' },
] as const;

export function BalanceHistorySection() {
  const { data: accountsData } = useListFinanceAccounts();
  const accounts = accountsData?.accounts ?? [];

  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [days, setDays] = useState<number>(90);
  const [stepChoice, setStepChoice] = useState<string>('');
  const [basis, setBasis] = useState<string>('snapshot');
  const ledger = basis === 'ledger';

  // Default to the first asset account (fall back to the first account overall)
  // until the user picks one explicitly.
  const defaultId =
    accounts.find((a) => a.class === 'asset')?.id ?? accounts[0]?.id ?? null;
  const activeId = selectedId ?? defaultId;

  const range = RANGES.find((r) => r.days === days) ?? RANGES[1];

  // The ledger basis has no raw form: open, close and flows only mean something
  // over a period, and the server reads an omitted step as `day` rather than
  // erroring. So "Raw readings" is dropped from the list under ledger, and
  // switching basis clears an already-selected 'raw' (below). Between those two
  // the step below can never be undefined under ledger, which is what stops the
  // select from claiming "Raw" over a daily series.
  const stepOptions = ledger
    ? STEP_OPTIONS.filter((o) => o.value !== 'raw')
    : STEP_OPTIONS;

  // Auto resolves to the range's step; Raw omits the param, which is the original
  // per-reading series.
  const step =
    stepChoice === 'raw'
      ? undefined
      : stepChoice === ''
        ? range.step
        : stepChoice;

  const chooseBasis = (next: string) => {
    setBasis(next);
    if (next === 'ledger' && stepChoice === 'raw') setStepChoice('');
  };

  const {
    data: history,
    isLoading,
    isError,
  } = useGetFinanceBalanceHistory(
    activeId ?? 0,
    { days, step, basis },
    { query: { enabled: activeId != null } },
  );

  const points = history?.points ?? [];

  // Series-level flags: one caption line under the chart rather than more ink on
  // the plot. drift_max is emitted under ledger even when it is 0, so the check
  // is against null and not against falsiness (a healthy series is the case
  // most worth showing).
  const caption = [
    points.length > 0 ? history?.note : undefined,
    history?.ledger_from
      ? `Ledger reaches back to ${formatDate(history.ledger_from)}`
      : undefined,
    history?.start_unverified
      ? 'The earliest opening is synthesized, so the ledger may not truly start there'
      : undefined,
    history?.drift_max != null
      ? `Largest drift against a reading ${formatMoney(history.drift_max)}`
      : undefined,
  ]
    .filter(Boolean)
    .join(' · ');

  return (
    <section
      className="flex flex-col gap-5 rounded-2xl border p-5 sm:p-6"
      style={citronCard}
    >
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <span
            className="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-xl text-citron"
            style={citronBadge}
          >
            <ChartGlyph />
          </span>
          <div>
            <h2 className="font-display text-lg font-bold text-white">
              Balance over time
            </h2>
            <p className="text-sm text-slate-400">
              {ledger
                ? 'Derived from posted transactions. Each bucket shows its close, with money in and out below.'
                : 'Snapshot history per account. Each bucket shows its last reading.'}
            </p>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <select
            aria-label="Account"
            className={`${selectClass} w-auto min-w-[10rem]`}
            value={activeId ?? ''}
            onChange={(e) => setSelectedId(Number(e.target.value))}
          >
            {accounts.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </select>

          <select
            aria-label="Bucket step"
            className={`${selectClass} w-auto`}
            value={stepChoice}
            onChange={(e) => setStepChoice(e.target.value)}
          >
            {stepOptions.map((o) => (
              <option key={o.value} value={o.value}>
                {o.value === '' ? `Auto (${range.step})` : o.label}
              </option>
            ))}
          </select>

          <div className="flex overflow-hidden rounded-lg border border-slate-700">
            {BASES.map((b) => (
              <button
                key={b.value}
                type="button"
                onClick={() => chooseBasis(b.value)}
                className={`px-2.5 py-1.5 text-xs font-medium transition ${
                  b.value === basis
                    ? 'bg-citron text-ink'
                    : 'text-slate-400 hover:text-white'
                }`}
              >
                {b.label}
              </button>
            ))}
          </div>

          <div className="flex overflow-hidden rounded-lg border border-slate-700">
            {RANGES.map((r) => {
              const active = r.days === days;
              return (
                <button
                  key={r.label}
                  type="button"
                  onClick={() => setDays(r.days)}
                  className={`px-2.5 py-1.5 text-xs font-medium transition ${
                    active
                      ? 'bg-citron text-ink'
                      : 'text-slate-400 hover:text-white'
                  }`}
                >
                  {r.label}
                </button>
              );
            })}
          </div>
        </div>
      </header>

      {accounts.length === 0 ? (
        <p className="py-8 text-center text-sm text-slate-400">No accounts.</p>
      ) : isError ? (
        <p className="py-8 text-center text-sm text-coral">
          Could not load balance history.
        </p>
      ) : isLoading ? (
        <p className="py-8 text-center text-sm text-slate-400">Loading…</p>
      ) : points.length === 0 ? (
        // A ledger series can come back empty on purpose (an investment account
        // is excluded, or there is no anchor reading to walk back from). The
        // server says why in `note`, and without it the section reads as broken.
        <p className="py-8 text-center text-sm text-slate-400">
          {history?.note ?? 'No balance history for this account yet.'}
        </p>
      ) : (
        <div>
          <BalanceChart points={points} basis={basis} />
          {caption !== '' && (
            <p className="mt-2 text-xs text-slate-500">{caption}</p>
          )}
        </div>
      )}
    </section>
  );
}

function ChartGlyph() {
  return (
    <svg
      width="20"
      height="20"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.9"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M3 3v18h18" />
      <path d="M7 14l4-4 3 3 5-6" />
    </svg>
  );
}
