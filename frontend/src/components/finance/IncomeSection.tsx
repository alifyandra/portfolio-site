'use client';

// Recurring income (ADR 0019): the owner's declared repeating receipts (salary, rent
// received, a regular transfer in), the sibling of the bills section in the opposite
// direction. Income is a DECLARATION, like a bill: creates and edits go to
// /api/admin/finance/income behind the server-enforced admin middleware, while the list
// comes from /api/finance/income.
//
// Receipts are linked by hand, by the matching pass from the income's match pattern (the
// Match button re-runs it over the whole ledger), or as evidence when a suggestion is
// accepted. The row shows when money last arrived once a link exists, and expands to
// list its receipts with a Remove action (IncomeReceiptList).
// The status and date rules live in incomeRules.ts, where node --test reaches them.

import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';

import {
  useListFinanceIncome,
  useCreateFinanceIncome,
  useUpdateFinanceIncome,
  useDeleteFinanceIncome,
  useReconcileFinanceIncome,
  useListFinanceAccounts,
  getListFinanceIncomeQueryKey,
  getListFinanceSuggestionsQueryKey,
} from '@/lib/api/generated';
import type { FinanceIncomeDTO } from '@/lib/api/model';
import {
  citronCard,
  citronBadge,
  inputClass,
  labelClass,
  selectClass,
  primaryBtn,
  ghostBtn,
  editBtn,
  dangerBtn,
} from '@/components/admin/ui';
import { formatDate } from './format';
import { AmountField, FigureSlot, useMoney } from './censor';
import { IncomeReceiptList } from './IncomeReceiptList';
import {
  melbourneTodayISO,
  pauseToggleTarget,
  canEnd,
  endPatch,
  scheduleText,
} from './incomeRules';

const CADENCES = [
  'weekly',
  'fortnightly',
  'monthly',
  'quarterly',
  'annual',
] as const;

type Cadence = (typeof CADENCES)[number];

const CADENCE_LABELS: Record<Cadence, string> = {
  weekly: 'Weekly',
  fortnightly: 'Fortnightly',
  monthly: 'Monthly',
  quarterly: 'Quarterly',
  annual: 'Annual',
};

interface IncomeForm {
  name: string;
  expected_amount: string;
  cadence: Cadence;
  anchor_date: string;
  account_id: string;
  match_pattern: string;
  notes: string;
}

// Neutral row-action button, sized like editBtn/dangerBtn (same as the bills section).
const rowGhostBtn =
  'rounded-md border border-slate-700 px-2.5 py-1 text-xs font-semibold text-white transition hover:border-slate-500 disabled:cursor-not-allowed disabled:opacity-50';

// A function, not a constant, so the default anchor is today when the form opens rather
// than whenever the module loaded.
function newIncomeForm(): IncomeForm {
  return {
    name: '',
    expected_amount: '',
    cadence: 'fortnightly',
    anchor_date: melbourneTodayISO(),
    account_id: '',
    match_pattern: '',
    notes: '',
  };
}

function incomeToForm(i: FinanceIncomeDTO): IncomeForm {
  return {
    name: i.name,
    expected_amount: String(i.expected_amount),
    cadence: i.cadence as Cadence,
    anchor_date: i.anchor_date,
    account_id: i.account_id == null ? '' : String(i.account_id),
    match_pattern: i.match_pattern,
    notes: i.notes,
  };
}

// The headline is quoted over the same window as the bills' committed figure, so the two
// sections read against each other.
const EXPECTED_WINDOW_DAYS = 30;

export function IncomeSection() {
  const queryClient = useQueryClient();
  const { money } = useMoney();
  // Two reads of one endpoint, as in the bills section: every status for the table, and
  // the active incomes paying inside the window for the headline.
  const { data, isLoading, isError } = useListFinanceIncome({ status: 'all' });
  const { data: dueSoon } = useListFinanceIncome({
    status: 'active',
    within_days: EXPECTED_WINDOW_DAYS,
  });
  const { data: accountsData } = useListFinanceAccounts();
  const accounts = accountsData?.accounts ?? [];

  const incomes = data?.income ?? [];
  const activeCount = incomes.filter((i) => i.status === 'active').length;

  // null = closed, 'new' = create form, number = editing that income id.
  const [editing, setEditing] = useState<number | 'new' | null>(null);
  const [form, setForm] = useState<IncomeForm>(newIncomeForm);
  // Which income's receipts are expanded; one at a time keeps the list fetch lazy.
  const [openReceipts, setOpenReceipts] = useState<number | null>(null);

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: getListFinanceIncomeQueryKey() });

  const create = useCreateFinanceIncome();
  const update = useUpdateFinanceIncome();
  const remove = useDeleteFinanceIncome();
  const reconcile = useReconcileFinanceIncome();

  const saving = create.isPending || update.isPending;
  const listBusy = remove.isPending || update.isPending || reconcile.isPending;
  const error = create.error ?? update.error ?? remove.error ?? reconcile.error;

  const patch = <K extends keyof IncomeForm>(key: K, value: IncomeForm[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  const openNew = () => {
    setForm(newIncomeForm());
    setEditing('new');
  };
  const openEdit = (i: FinanceIncomeDTO) => {
    setForm(incomeToForm(i));
    setEditing(i.id);
  };
  const close = () => setEditing(null);

  // The API caps name at 120 BYTES, so count bytes here too: accented characters cost
  // more than one.
  const nameBytes = new TextEncoder().encode(form.name.trim()).length;
  const patternBytes = new TextEncoder().encode(
    form.match_pattern.trim(),
  ).length;
  const canSave =
    nameBytes > 0 &&
    nameBytes <= 120 &&
    patternBytes <= 200 &&
    Number.parseFloat(form.expected_amount) > 0 &&
    form.anchor_date.length > 0 &&
    !saving;

  const save = () => {
    if (!canSave) return;
    const payload = {
      name: form.name.trim(),
      expected_amount: Number.parseFloat(form.expected_amount),
      cadence: form.cadence,
      anchor_date: form.anchor_date,
      // 0 detaches the account on a PATCH and means "unset" on a create.
      account_id: form.account_id ? Number.parseInt(form.account_id, 10) : 0,
      match_pattern: form.match_pattern.trim(),
      notes: form.notes,
    };
    const onSuccess = () => {
      invalidate();
      close();
    };
    if (editing === 'new') {
      create.mutate({ data: payload }, { onSuccess });
    } else if (typeof editing === 'number') {
      update.mutate({ id: editing, data: payload }, { onSuccess });
    }
  };

  const togglePause = (i: FinanceIncomeDTO) => {
    const status = pauseToggleTarget(i.status);
    if (status === null) return;
    update.mutate({ id: i.id, data: { status } }, { onSuccess: invalidate });
  };

  // Ending takes a date, defaulting to today; the API checks its format and refuses an
  // end without one.
  const end = (i: FinanceIncomeDTO) => {
    const endedOn = prompt(
      `End "${i.name}" on which date? (YYYY-MM-DD; no pay date after it is expected)`,
      melbourneTodayISO(),
    )?.trim();
    if (!endedOn) return;
    update.mutate(
      { id: i.id, data: endPatch(endedOn) },
      { onSuccess: invalidate },
    );
  };

  const del = (i: FinanceIncomeDTO) => {
    if (!confirm(`Delete "${i.name}"? Its receipt links go too.`)) return;
    remove.mutate(
      { id: i.id },
      {
        onSuccess: () => {
          invalidate();
          // An income accepted from a suggestion reopens it on delete.
          queryClient.invalidateQueries({
            queryKey: getListFinanceSuggestionsQueryKey(),
          });
        },
      },
    );
  };

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
            <IncomeGlyph />
          </span>
          <div>
            <h2 className="font-display text-lg font-bold text-white">
              Recurring income
            </h2>
            <p className="text-sm text-slate-400">
              Declared receipts: what is expected, and when.
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            className={ghostBtn}
            disabled={reconcile.isPending}
            onClick={() =>
              reconcile.mutate(undefined, { onSuccess: invalidate })
            }
          >
            {reconcile.isPending ? 'Matching…' : 'Match'}
          </button>
          <button type="button" className={primaryBtn} onClick={openNew}>
            Add income
          </button>
        </div>
      </header>

      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-lg border border-slate-700 bg-deepsea/40 px-3 py-2.5">
        <FigureSlot className="text-lg font-semibold tabular-nums text-citron">
          {money(dueSoon?.expected_total ?? 0)}
        </FigureSlot>
        <span className="text-sm text-slate-300">
          expected over the next {EXPECTED_WINDOW_DAYS} days
        </span>
        <span className="text-xs text-slate-400">
          <FigureSlot className="tabular-nums">
            {money(data?.monthly_equivalent ?? 0)}
          </FigureSlot>{' '}
          a month across {activeCount} active{' '}
          {activeCount === 1 ? 'income' : 'incomes'}
        </span>
      </div>

      {error ? (
        <p className="text-sm text-coral">{(error as Error).message}</p>
      ) : null}

      {editing !== null ? (
        <div className="flex flex-col gap-3 rounded-lg border border-slate-700 bg-deepsea/40 p-3">
          <div className="grid gap-3 sm:grid-cols-2">
            <label className={labelClass}>
              Name
              <input
                type="text"
                className={inputClass}
                value={form.name}
                onChange={(e) => patch('name', e.target.value)}
              />
            </label>
            <label className={labelClass}>
              Expected amount
              <AmountField
                fieldLabel="expected amount"
                step="0.01"
                min="0"
                className={inputClass}
                value={form.expected_amount}
                onChange={(next) => patch('expected_amount', next)}
              />
            </label>
            <label className={labelClass}>
              Cadence
              <select
                className={selectClass}
                value={form.cadence}
                onChange={(e) => patch('cadence', e.target.value as Cadence)}
              >
                {CADENCES.map((c) => (
                  <option key={c} value={c}>
                    {CADENCE_LABELS[c]}
                  </option>
                ))}
              </select>
            </label>
            <label className={labelClass}>
              Anchor date (one known pay date)
              <input
                type="date"
                className={inputClass}
                value={form.anchor_date}
                onChange={(e) => patch('anchor_date', e.target.value)}
              />
            </label>
            <label className={labelClass}>
              Paid into (optional)
              <select
                className={selectClass}
                value={form.account_id}
                onChange={(e) => patch('account_id', e.target.value)}
              >
                <option value="">Any account</option>
                {accounts.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}
                  </option>
                ))}
              </select>
            </label>
            <label className={`${labelClass} sm:col-span-2`}>
              Match pattern (optional; text in the credit&apos;s description)
              <input
                type="text"
                className={inputClass}
                value={form.match_pattern}
                onChange={(e) => patch('match_pattern', e.target.value)}
              />
            </label>
            <label className={`${labelClass} sm:col-span-2`}>
              Notes
              <textarea
                rows={2}
                className={inputClass}
                value={form.notes}
                onChange={(e) => patch('notes', e.target.value)}
              />
            </label>
          </div>
          {patternBytes > 200 ? (
            <p className="text-xs text-coral">
              Match pattern is too long (at most 200 bytes).
            </p>
          ) : null}
          {nameBytes > 120 ? (
            <p className="text-xs text-coral">
              Name is too long (at most 120 bytes; accented characters count
              more than one).
            </p>
          ) : null}
          <div className="flex items-center gap-2">
            <button
              type="button"
              className={primaryBtn}
              disabled={!canSave}
              onClick={save}
            >
              {saving ? 'Saving…' : editing === 'new' ? 'Add income' : 'Save'}
            </button>
            <button type="button" className={ghostBtn} onClick={close}>
              Cancel
            </button>
          </div>
        </div>
      ) : null}

      {isError ? (
        <p className="text-sm text-coral">Could not load recurring income.</p>
      ) : isLoading ? (
        <p className="text-sm text-slate-400">Loading income…</p>
      ) : incomes.length === 0 ? (
        <p className="text-sm text-slate-400">
          No recurring income declared yet.
        </p>
      ) : (
        <ul className="flex flex-col gap-2">
          {incomes.map((i) => (
            <li
              key={i.id}
              className="flex flex-col gap-2 rounded-lg border border-slate-700 bg-deepsea/40 p-3"
            >
              <div className="flex items-start justify-between gap-4">
                <div className="flex min-w-0 flex-col gap-0.5">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate font-medium text-white">
                      {i.name}
                    </span>
                    <span className="font-mono text-xs uppercase tracking-wide text-slate-400">
                      {CADENCE_LABELS[i.cadence as Cadence] ?? i.cadence}
                    </span>
                    {i.status !== 'active' ? (
                      <span className="rounded-sm bg-white/5 px-1.5 py-0.5 text-xs text-slate-300">
                        {i.status}
                      </span>
                    ) : null}
                  </div>
                  <span className="truncate text-xs text-slate-400">
                    {scheduleText(i, formatDate)}
                    {i.account_name ? ` · ${i.account_name}` : ''}
                  </span>
                  <span className="truncate text-xs text-slate-400">
                    {i.last_received_date && i.last_received_amount != null ? (
                      <>
                        Last received {formatDate(i.last_received_date)} ·{' '}
                        {money(i.last_received_amount)}
                      </>
                    ) : (
                      'No receipt linked yet'
                    )}
                  </span>
                </div>

                <div className="flex shrink-0 flex-col items-end gap-1.5">
                  <span className="text-sm font-semibold tabular-nums text-white">
                    {money(i.expected_amount)}
                  </span>
                  <div className="flex items-center gap-1.5">
                    <button
                      type="button"
                      className={editBtn}
                      disabled={listBusy}
                      onClick={() => openEdit(i)}
                    >
                      Edit
                    </button>
                    {pauseToggleTarget(i.status) !== null ? (
                      <button
                        type="button"
                        className={rowGhostBtn}
                        disabled={listBusy}
                        onClick={() => togglePause(i)}
                      >
                        {i.status === 'paused' ? 'Resume' : 'Pause'}
                      </button>
                    ) : null}
                    {canEnd(i.status) ? (
                      <button
                        type="button"
                        className={rowGhostBtn}
                        disabled={listBusy}
                        onClick={() => end(i)}
                      >
                        End
                      </button>
                    ) : null}
                    <button
                      type="button"
                      className={rowGhostBtn}
                      aria-expanded={openReceipts === i.id}
                      onClick={() =>
                        setOpenReceipts((cur) => (cur === i.id ? null : i.id))
                      }
                    >
                      {openReceipts === i.id ? 'Hide receipts' : 'Receipts'}
                    </button>
                    <button
                      type="button"
                      className={dangerBtn}
                      disabled={listBusy}
                      onClick={() => del(i)}
                    >
                      Delete
                    </button>
                  </div>
                </div>
              </div>
              {openReceipts === i.id ? (
                <div className="border-t border-slate-700 pt-2">
                  <IncomeReceiptList incomeId={i.id} incomeName={i.name} />
                </div>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function IncomeGlyph() {
  return (
    <svg
      width="18"
      height="18"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.9"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M12 3v12M7 10l5 5 5-5" />
      <path d="M4 15v4a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-4" />
    </svg>
  );
}
