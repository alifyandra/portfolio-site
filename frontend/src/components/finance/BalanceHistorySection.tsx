'use client';

// Balance-over-time: an account selector (defaulting to the first asset account)
// + a look-back range, feeding the hand-rolled BalanceChart. The accounts query
// is shared with AccountsSection (React Query dedupes by key), so this adds no
// extra round-trip for the account list.

import { useState } from 'react';

import {
  useListFinanceAccounts,
  useGetFinanceBalanceHistory,
} from '@/lib/api/generated';
import { citronCard, citronBadge, selectClass } from '@/components/admin/ui';
import { BalanceChart } from './BalanceChart';

const RANGES = [
  { label: '30d', days: 30 },
  { label: '90d', days: 90 },
  { label: '1y', days: 365 },
  { label: 'All', days: 0 },
] as const;

export function BalanceHistorySection() {
  const { data: accountsData } = useListFinanceAccounts();
  const accounts = accountsData?.accounts ?? [];

  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [days, setDays] = useState<number>(90);

  // Default to the first asset account (fall back to the first account overall)
  // until the user picks one explicitly.
  const defaultId =
    accounts.find((a) => a.class === 'asset')?.id ?? accounts[0]?.id ?? null;
  const activeId = selectedId ?? defaultId;

  const {
    data: history,
    isLoading,
    isError,
  } = useGetFinanceBalanceHistory(
    activeId ?? 0,
    { days },
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
            <p className="text-sm text-slate-400">Snapshot history per account.</p>
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
