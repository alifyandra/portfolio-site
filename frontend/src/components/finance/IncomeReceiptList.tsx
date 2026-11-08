'use client';

// One income's receipts: each pay cycle that has been linked to the posted credit that
// paid it, newest first, with a Remove action. Removing takes only the link back (the
// transaction is ledger data and stays), so it always asks first. The list is fetched
// only while the row is expanded.

import { useQueryClient } from '@tanstack/react-query';

import {
  useListFinanceIncomeReceipts,
  useDeleteFinanceIncomeReceipt,
  getListFinanceIncomeReceiptsQueryKey,
  getListFinanceIncomeQueryKey,
} from '@/lib/api/generated';
import { dangerBtn } from '@/components/admin/ui';
import { formatDate } from './format';
import { useMoney } from './censor';
import { paidTimingLabel, removeReceiptPrompt } from './incomeRules';

export function IncomeReceiptList({
  incomeId,
  incomeName,
}: {
  incomeId: number;
  incomeName: string;
}) {
  const queryClient = useQueryClient();
  const { money } = useMoney();
  const { data, isLoading, isError } = useListFinanceIncomeReceipts(incomeId);
  const remove = useDeleteFinanceIncomeReceipt();

  const receipts = data?.receipts ?? [];

  const onRemove = (receiptId: number, occurrenceDate: string) => {
    if (!confirm(removeReceiptPrompt(incomeName, occurrenceDate, formatDate))) {
      return;
    }
    remove.mutate(
      { id: incomeId, receiptId },
      {
        onSuccess: () => {
          // The row's "last received" comes from the newest receipt, so the income list
          // goes stale with this one.
          queryClient.invalidateQueries({
            queryKey: getListFinanceIncomeReceiptsQueryKey(incomeId),
          });
          queryClient.invalidateQueries({
            queryKey: getListFinanceIncomeQueryKey(),
          });
        },
      },
    );
  };

  if (isError) {
    return <p className="text-xs text-coral">Could not load the receipts.</p>;
  }
  if (isLoading) {
    return <p className="text-xs text-slate-400">Loading receipts…</p>;
  }
  if (receipts.length === 0) {
    return <p className="text-xs text-slate-400">No receipts linked.</p>;
  }

  return (
    <div className="flex flex-col gap-1.5">
      {remove.error ? (
        <p className="text-xs text-coral">{(remove.error as Error).message}</p>
      ) : null}
      <ul className="flex flex-col divide-y divide-slate-700/60">
        {receipts.map((r) => (
          <li
            key={r.id}
            className="flex items-center justify-between gap-3 py-1.5 text-xs"
          >
            <div className="flex min-w-0 flex-col">
              <span className="text-slate-200">
                Pay date {formatDate(r.occurrence_date)}
                {r.method === 'auto' ? (
                  <span className="ml-1.5 rounded-sm bg-white/5 px-1 py-0.5 text-[10px] uppercase tracking-wide text-slate-400">
                    auto
                  </span>
                ) : null}
              </span>
              <span className="truncate text-slate-400">
                Posted {formatDate(r.posted_date)} ·{' '}
                {paidTimingLabel(r.occurrence_date, r.posted_date)} ·{' '}
                {r.description}
              </span>
            </div>
            <div className="flex shrink-0 items-center gap-2">
              <span className="tabular-nums text-white">{money(r.amount)}</span>
              <button
                type="button"
                className={dangerBtn}
                disabled={remove.isPending}
                onClick={() => onRemove(r.id, r.occurrence_date)}
              >
                Remove
              </button>
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}
