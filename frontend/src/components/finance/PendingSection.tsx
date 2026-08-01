'use client';

// Pending (authorised-but-not-posted) transactions across all accounts. These
// are volatile — they change or vanish as the bank settles them — so the section
// is visually set apart from the posted ledger with a live/pulse cue and a
// sky-tinted surface rather than the citron admin family.

import { useListFinancePending } from '@/lib/api/generated';
import type { FinancePendingDTO } from '@/lib/api/model';
import { rowClass } from '@/components/admin/ui';
import { formatDate } from './format';
import { useMoney } from './censor';

const skyCard = {
  borderColor: 'color-mix(in srgb, var(--color-sky) 34%, transparent)',
  background: 'color-mix(in srgb, var(--color-sky) 7%, var(--color-deepsea))',
};

export function PendingSection() {
  const { data, isLoading, isError } = useListFinancePending();
  const pending = data?.pending ?? [];

  return (
    <section
      className="flex flex-col gap-4 rounded-2xl border p-5 sm:p-6"
      style={skyCard}
    >
      <header className="flex items-center gap-2">
        <span className="relative inline-flex h-2.5 w-2.5">
          <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-sky opacity-60" />
          <span className="relative inline-flex h-2.5 w-2.5 rounded-full bg-sky" />
        </span>
        <h2 className="font-display text-lg font-bold text-white">Pending</h2>
        <span className="font-mono text-xs uppercase tracking-widest text-sky">
          live · unsettled
        </span>
      </header>

      {isError ? (
        <p className="text-sm text-coral">Could not load pending activity.</p>
      ) : isLoading ? (
        <p className="text-sm text-slate-400">Loading…</p>
      ) : pending.length === 0 ? (
        <p className="text-sm text-slate-400">Nothing pending right now.</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {pending.map((p) => (
            <li key={p.id} className={rowClass}>
              <PendingRow item={p} />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function PendingRow({ item }: { item: FinancePendingDTO }) {
  const { money, censored } = useMoney();
  // Same sign-neutralisation as a posted row: no tint, no "+", no minus.
  const isOut = item.amount < 0;
  const toneClass = censored
    ? 'text-slate-200'
    : isOut
      ? 'text-coral'
      : 'text-mint';
  const primary = item.merchant || item.description || '—';

  return (
    <>
      <div className="flex min-w-0 flex-col">
        <span className="truncate text-sm text-white">{primary}</span>
        <span className="truncate text-xs text-slate-400">
          {item.account_name} · {formatDate(item.date)}
        </span>
      </div>
      <span
        className={`shrink-0 text-sm font-semibold tabular-nums ${toneClass}`}
      >
        {censored || isOut ? '' : '+'}
        {money(item.amount)}
      </span>
    </>
  );
}
