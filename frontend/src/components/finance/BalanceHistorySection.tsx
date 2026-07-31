'use client';

// Balance-over-time: an account selector (defaulting to the first asset account)
// + a look-back range + a bucket step, feeding the hand-rolled BalanceChart. The
// accounts query is shared with AccountsSection (React Query dedupes by key), so
// this adds no extra round-trip for the account list.
//
// Each range carries a default step, because a 1y view of raw readings plots
// roughly 365 near-identical points into a few hundred pixels: wasted work and a
// noisier line than the trend deserves. The step select overrides it, and "Raw"
// asks for the unbucketed per-reading series.

import { useState } from 'react';

import {
  useListFinanceAccounts,
  useGetFinanceBalanceHistory,
} from '@/lib/api/generated';
import { citronCard, citronBadge, selectClass } from '@/components/admin/ui';
import { BalanceChart } from './BalanceChart';

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

export function BalanceHistorySection() {
  const { data: accountsData } = useListFinanceAccounts();
  const accounts = accountsData?.accounts ?? [];

  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [days, setDays] = useState<number>(90);
  const [stepChoice, setStepChoice] = useState<string>('');

  // Default to the first asset account (fall back to the first account overall)
  // until the user picks one explicitly.
  const defaultId =
    accounts.find((a) => a.class === 'asset')?.id ?? accounts[0]?.id ?? null;
  const activeId = selectedId ?? defaultId;

  const range = RANGES.find((r) => r.days === days) ?? RANGES[1];
  // Auto resolves to the range's step; Raw omits the param, which is the original
  // per-reading series.
  const step =
    stepChoice === 'raw'
      ? undefined
      : stepChoice === ''
        ? range.step
        : stepChoice;

  const {
    data: history,
    isLoading,
    isError,
  } = useGetFinanceBalanceHistory(
    activeId ?? 0,
    { days, step },
    { query: { enabled: activeId != null } },
  );

  const points = history?.points ?? [];

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
              Snapshot history per account. Each bucket shows its last reading.
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
            {STEP_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.value === '' ? `Auto (${range.step})` : o.label}
              </option>
            ))}
          </select>

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
      ) : (
        <BalanceChart points={points} />
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
