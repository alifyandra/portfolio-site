'use client';

// Net-worth hero: the headline figure for the private finance dashboard, with
// assets / liabilities sub-figures and a staleness stamp. Reads the summary DTO
// (server-computed net_worth = assets - liabilities). Assets and liabilities are
// both delivered as positive magnitudes; liabilities are what's owed.

import { useGetFinanceSummary } from '@/lib/api/generated';
import { citronCard, citronBadge } from '@/components/admin/ui';
import { formatMoney, formatAbs, formatDateTime } from './format';

export function NetWorthHero() {
  const { data, isLoading, isError } = useGetFinanceSummary();

  return (
    <section
      className="flex flex-col gap-5 rounded-2xl border p-6 sm:p-8"
      style={citronCard}
    >
      <div className="flex items-center gap-3">
        <span
          className="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-xl text-citron"
          style={citronBadge}
        >
          <WalletGlyph />
        </span>
        <div>
          <p className="font-mono text-xs uppercase tracking-widest text-citron">
            net worth
          </p>
          {data?.as_of ? (
            <p className="text-sm text-slate-400">
              as of {formatDateTime(data.as_of)}
            </p>
          ) : (
            <p className="text-sm text-slate-400">
              {isLoading ? 'loading…' : 'no snapshots yet'}
            </p>
          )}
        </div>
      </div>

      {isError ? (
        <p className="text-sm text-coral">Could not load the summary.</p>
      ) : (
        <>
          <p className="font-display text-4xl font-bold tabular-nums text-white sm:text-5xl">
            {isLoading || !data ? '—' : formatMoney(data.net_worth)}
          </p>

          <div className="grid grid-cols-2 gap-4 sm:max-w-md">
            <div className="flex flex-col gap-1">
              <span className="font-mono text-[0.65rem] uppercase tracking-widest text-slate-400">
                assets
              </span>
              <span className="font-display text-xl font-semibold tabular-nums text-mint">
                {isLoading || !data ? '—' : formatMoney(data.assets)}
              </span>
            </div>
            <div className="flex flex-col gap-1">
              <span className="font-mono text-[0.65rem] uppercase tracking-widest text-slate-400">
                liabilities
              </span>
              <span className="font-display text-xl font-semibold tabular-nums text-coral">
                {isLoading || !data ? '—' : `${formatAbs(data.liabilities)} owed`}
              </span>
            </div>
          </div>

          {data && (
            <p className="text-xs text-slate-400">
              Across {data.account_count} account
              {data.account_count === 1 ? '' : 's'} · {data.currency}
            </p>
          )}
        </>
      )}
    </section>
  );
}

function WalletGlyph() {
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
      <rect x="3" y="6" width="18" height="13" rx="2" />
      <path d="M3 10h18M16 14.5h.01" />
    </svg>
  );
}
