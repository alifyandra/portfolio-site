'use client';

// Accounts list for the finance dashboard: one row per account with a type
// badge, masked number and its latest snapshot balance. For liability accounts
// (class=liability) the snapshot balance is negative = owed, so we show the
// magnitude in coral labelled "owed"; assets show their balance plainly.

import { useListFinanceAccounts } from '@/lib/api/generated';
import type { FinanceAccountDTO } from '@/lib/api/model';
import { citronCard, citronBadge, rowClass } from '@/components/admin/ui';
import { formatMoney, formatAbs, formatDate, accountTypeLabel } from './format';

export function AccountsSection() {
  const { data, isLoading, isError } = useListFinanceAccounts();
  const accounts = data?.accounts ?? [];

  return (
    <section
      className="flex flex-col gap-5 rounded-2xl border p-5 sm:p-6"
      style={citronCard}
    >
      <SectionHeader
        title="Accounts"
        subtitle="Latest snapshot balance per account."
      />

      {isError ? (
        <p className="text-sm text-coral">Could not load accounts.</p>
      ) : isLoading ? (
        <p className="text-sm text-slate-400">Loading accounts…</p>
      ) : accounts.length === 0 ? (
        <p className="text-sm text-slate-400">No accounts.</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {accounts.map((acct) => (
            <li key={acct.id} className={rowClass}>
              <AccountRow account={acct} />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function AccountRow({ account }: { account: FinanceAccountDTO }) {
  const isLiability = account.class === 'liability';

  return (
    <>
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate font-medium text-white">{account.name}</span>
          <TypeBadge type={account.type} />
        </div>
        <span className="font-mono text-xs text-slate-400">
          {account.masked_number || '—'}
        </span>
        {(account.available != null || account.credit_limit != null) && (
          <span className="text-xs text-slate-400">
            {account.available != null && (
              <>Available {formatMoney(account.available)}</>
            )}
            {account.available != null && account.credit_limit != null && ' · '}
            {account.credit_limit != null && (
              <>Limit {formatMoney(account.credit_limit)}</>
            )}
          </span>
        )}
      </div>

      <div className="flex shrink-0 flex-col items-end gap-0.5">
        {account.balance == null ? (
          <span className="text-sm text-slate-400">no snapshot</span>
        ) : isLiability ? (
          <span className="font-display text-lg font-semibold tabular-nums text-coral">
            {formatAbs(account.balance)}
            <span className="ml-1 text-xs font-normal text-slate-400">owed</span>
          </span>
        ) : (
          <span className="font-display text-lg font-semibold tabular-nums text-white">
            {formatMoney(account.balance)}
          </span>
        )}
        {account.balance_as_of && (
          <span className="text-[0.65rem] text-slate-400">
            {formatDate(account.balance_as_of)}
          </span>
        )}
      </div>
    </>
  );
}

function TypeBadge({ type }: { type: FinanceAccountDTO['type'] }) {
  return (
    <span className="rounded-full border border-slate-600 px-2 py-0.5 text-[0.65rem] font-medium uppercase tracking-wide text-slate-300">
      {accountTypeLabel(type)}
    </span>
  );
}

function SectionHeader({ title, subtitle }: { title: string; subtitle: string }) {
  return (
    <header className="flex items-center gap-3">
      <span
        className="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-xl text-citron"
        style={citronBadge}
      >
        <StackGlyph />
      </span>
      <div>
        <h2 className="font-display text-lg font-bold text-white">{title}</h2>
        <p className="text-sm text-slate-400">{subtitle}</p>
      </div>
    </header>
  );
}

function StackGlyph() {
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
      <path d="M3 7l9-4 9 4-9 4-9-4Z" />
      <path d="M3 12l9 4 9-4M3 17l9 4 9-4" />
    </svg>
  );
}
