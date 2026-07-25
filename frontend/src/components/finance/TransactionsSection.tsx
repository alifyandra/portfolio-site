'use client';

// Recent transactions: the newest 50 rows (server-ordered), with an account
// filter. Built as a <ul>/<li> table (the codebase has no Table component).
// Amount is signed: money in is mint/positive, money out is coral/negative.

import { useState } from 'react';

import {
  useListFinanceTransactions,
  useListFinanceAccounts,
} from '@/lib/api/generated';
import type { FinanceTxnDTO } from '@/lib/api/model';
import { citronCard, citronBadge, selectClass } from '@/components/admin/ui';
import { formatMoney, formatDate } from './format';

const LIMIT = 50;

export function TransactionsSection() {
  const { data: accountsData } = useListFinanceAccounts();
  const accounts = accountsData?.accounts ?? [];

  // 0 = all accounts (the API treats account_id 0 / omitted as "span all").
  const [accountId, setAccountId] = useState<number>(0);

  const { data, isLoading, isError } = useListFinanceTransactions({
    limit: LIMIT,
    account_id: accountId || undefined,
  });

  const transactions = data?.transactions ?? [];
  const total = data?.total ?? 0;

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
            <ListGlyph />
          </span>
          <div>
            <h2 className="font-display text-lg font-bold text-white">
              Recent transactions
            </h2>
            <p className="text-sm text-slate-400">Newest posted activity.</p>
          </div>
        </div>

        <select
          aria-label="Filter by account"
          className={`${selectClass} w-auto min-w-[10rem]`}
          value={accountId}
          onChange={(e) => setAccountId(Number(e.target.value))}
        >
          <option value={0}>All accounts</option>
          {accounts.map((a) => (
            <option key={a.id} value={a.id}>
              {a.name}
            </option>
          ))}
        </select>
      </header>

      {isError ? (
        <p className="text-sm text-coral">Could not load transactions.</p>
      ) : isLoading ? (
        <p className="text-sm text-slate-400">Loading transactions…</p>
      ) : transactions.length === 0 ? (
        <p className="text-sm text-slate-400">No transactions.</p>
      ) : (
        <>
          <ul className="flex flex-col divide-y divide-slate-800 rounded-lg border border-slate-700 bg-deepsea/40">
            {transactions.map((txn) => (
              <li key={txn.id}>
                <TransactionRow txn={txn} />
              </li>
            ))}
          </ul>
          <p className="text-xs text-slate-400">
            Showing {transactions.length} of {total.toLocaleString('en-AU')}
          </p>
        </>
      )}
    </section>
  );
}

function TransactionRow({ txn }: { txn: FinanceTxnDTO }) {
  const isOut = txn.amount < 0;
  const primary = txn.merchant || txn.description || '—';
  const secondary =
    txn.merchant && txn.description && txn.merchant !== txn.description
      ? txn.description
      : null;

  return (
    <div className="flex items-center gap-3 px-3 py-2.5">
      <span className="w-16 shrink-0 text-xs text-slate-400">
        {formatDate(txn.posted_date)}
      </span>

      <div className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-sm text-white">{primary}</span>
        <span className="truncate text-xs text-slate-400">
          {txn.account_name}
          {secondary ? ` · ${secondary}` : ''}
        </span>
      </div>

      <div className="flex shrink-0 flex-col items-end">
        <span
          className={`text-sm font-semibold tabular-nums ${
            isOut ? 'text-coral' : 'text-mint'
          }`}
        >
          {isOut ? '' : '+'}
          {formatMoney(txn.amount)}
        </span>
        {txn.balance_after != null && (
          <span className="text-[0.65rem] tabular-nums text-slate-400">
            bal {formatMoney(txn.balance_after)}
          </span>
        )}
      </div>
    </div>
  );
}

function ListGlyph() {
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
      <path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01" />
    </svg>
  );
}
