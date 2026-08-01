'use client';

// Recurring bills (portfolio-site#125): the owner's declared repeating commitments
// (rent, insurance, subscriptions, utilities) with the money they have already spoken
// for. A bill is a DECLARATION, not a ledger row, so this is the one section on
// /finance that writes: creates and edits go to /api/admin/finance/bills behind the
// server-enforced admin middleware, while the list comes from /api/finance/bills.
//
// The committed-money line at the top is the number the whole feature exists to
// produce. Deliberately no charts and no calendar: a flat table plus an inline form.

import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';

import {
  useListFinanceBills,
  useCreateFinanceBill,
  useUpdateFinanceBill,
  useDeleteFinanceBill,
  useReconcileFinanceBills,
  useListFinanceAccounts,
  getListFinanceBillsQueryKey,
} from '@/lib/api/generated';
import type { FinanceBillDTO } from '@/lib/api/model';
import { UpdateBillInputBodyStatus } from '@/lib/api/model';
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
  rowClass,
} from '@/components/admin/ui';
import { formatDate } from './format';
import { AmountField, FigureSlot, useMoney } from './censor';

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

interface BillForm {
  name: string;
  payee: string;
  expected_amount: string;
  cadence: Cadence;
  anchor_date: string;
  amount_variable: boolean;
  amount_tolerance_pct: string;
  match_pattern: string;
  match_window_days: string;
  account_id: string;
  notes: string;
}

function todayISO(): string {
  return new Date().toISOString().slice(0, 10);
}

// Neutral row-action button, sized like editBtn/dangerBtn so the pause toggle sits with
// them without claiming an accent colour.
const rowGhostBtn =
  'rounded-md border border-slate-700 px-2.5 py-1 text-xs font-semibold text-white transition hover:border-slate-500 disabled:cursor-not-allowed disabled:opacity-50';

const emptyForm: BillForm = {
  name: '',
  payee: '',
  expected_amount: '',
  cadence: 'monthly',
  anchor_date: todayISO(),
  amount_variable: false,
  amount_tolerance_pct: '10',
  match_pattern: '',
  match_window_days: '5',
  account_id: '',
  notes: '',
};

function billToForm(b: FinanceBillDTO): BillForm {
  return {
    name: b.name,
    payee: b.payee,
    expected_amount: String(b.expected_amount),
    cadence: b.cadence as Cadence,
    anchor_date: b.anchor_date,
    amount_variable: b.amount_variable,
    amount_tolerance_pct: String(b.amount_tolerance_pct),
    match_pattern: b.match_pattern,
    match_window_days: String(b.match_window_days),
    account_id: b.account_id == null ? '' : String(b.account_id),
    notes: b.notes,
  };
}

/** "in 4 days" / "today" / "8 days ago", from the signed days_until. */
function dueLabel(days: number): string {
  if (days === 0) return 'today';
  if (days > 0) return `in ${days} day${days === 1 ? '' : 's'}`;
  const late = -days;
  return `${late} day${late === 1 ? '' : 's'} ago`;
}

// COMMITTED_WINDOW_DAYS is the horizon the headline figure is quoted over. Committed money
// only means something across a stated window: summing raw per-cycle amounts over mixed
// cadences (a weekly 120 plus an annual 1200) is not a number anyone can act on.
const COMMITTED_WINDOW_DAYS = 30;

export function BillsSection() {
  const queryClient = useQueryClient();
  const { money, censored } = useMoney();
  // Two reads of the same endpoint, deliberately. The table wants every status so a paused
  // or ended commitment stays visible and editable; the headline wants the ACTIVE bills
  // actually falling due inside the window, which is the number the section exists to
  // produce. Each figure is then captioned with the set it was computed over.
  const { data, isLoading, isError } = useListFinanceBills({ status: 'all' });
  const { data: dueSoon } = useListFinanceBills({
    status: 'active',
    within_days: COMMITTED_WINDOW_DAYS,
  });
  const { data: accountsData } = useListFinanceAccounts();
  const accounts = accountsData?.accounts ?? [];

  const bills = data?.bills ?? [];
  // monthly_equivalent from the status=all read already counts active bills only (the API
  // keeps paused and ended out of the money), so this caption counts the same set.
  const activeCount = bills.filter((b) => b.status === 'active').length;

  // null = closed, 'new' = create form, number = editing that bill id.
  const [editing, setEditing] = useState<number | 'new' | null>(null);
  const [form, setForm] = useState<BillForm>(emptyForm);

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: getListFinanceBillsQueryKey() });

  const create = useCreateFinanceBill();
  const update = useUpdateFinanceBill();
  const remove = useDeleteFinanceBill();
  const reconcile = useReconcileFinanceBills();

  const saving = create.isPending || update.isPending;
  const listBusy = remove.isPending || update.isPending || reconcile.isPending;
  const error = create.error ?? update.error ?? remove.error ?? reconcile.error;

  const patch = <K extends keyof BillForm>(key: K, value: BillForm[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  const openNew = () => {
    setForm(emptyForm);
    setEditing('new');
  };
  const openEdit = (b: FinanceBillDTO) => {
    setForm(billToForm(b));
    setEditing(b.id);
  };
  const close = () => setEditing(null);

  const canSave =
    form.name.trim().length > 0 &&
    Number.parseFloat(form.expected_amount) > 0 &&
    form.anchor_date.length > 0 &&
    !saving;

  const save = () => {
    if (!canSave) return;
    const payload = {
      name: form.name.trim(),
      payee: form.payee.trim(),
      expected_amount: Number.parseFloat(form.expected_amount),
      cadence: form.cadence,
      anchor_date: form.anchor_date,
      amount_variable: form.amount_variable,
      amount_tolerance_pct: Number.parseFloat(form.amount_tolerance_pct) || 0,
      match_pattern: form.match_pattern.trim(),
      match_window_days: Number.parseInt(form.match_window_days, 10) || 0,
      // 0 detaches the account on a PATCH and means "unset" on a create.
      account_id: form.account_id ? Number.parseInt(form.account_id, 10) : 0,
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

  const togglePause = (b: FinanceBillDTO) => {
    const status =
      b.status === 'paused'
        ? UpdateBillInputBodyStatus.active
        : UpdateBillInputBodyStatus.paused;
    update.mutate({ id: b.id, data: { status } }, { onSuccess: invalidate });
  };

  const del = (b: FinanceBillDTO) => {
    if (!confirm(`Delete "${b.name}"? Its reconciliation links go too.`)) return;
    remove.mutate({ id: b.id }, { onSuccess: invalidate });
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
            <BillGlyph />
          </span>
          <div>
            <h2 className="font-display text-lg font-bold text-white">
              Recurring bills
            </h2>
            <p className="text-sm text-slate-400">
              Declared commitments, reconciled against the ledger.
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
            {reconcile.isPending ? 'Reconciling…' : 'Reconcile'}
          </button>
          <button type="button" className={primaryBtn} onClick={openNew}>
            Add bill
          </button>
        </div>
      </header>

      {/* Committed money: the figure the whole section exists to produce. Each number is
          captioned with exactly the set it counts. */}
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-lg border border-slate-700 bg-deepsea/40 px-3 py-2.5">
        <FigureSlot className="text-lg font-semibold tabular-nums text-citron">
          {money(dueSoon?.committed_total ?? 0)}
        </FigureSlot>
        <span className="text-sm text-slate-300">
          committed over the next {COMMITTED_WINDOW_DAYS} days
        </span>
        <span className="text-xs text-slate-400">
          {dueSoon?.count ?? 0} {dueSoon?.count === 1 ? 'bill' : 'bills'} due in
          that window ·{' '}
          <FigureSlot className="tabular-nums">
            {money(data?.monthly_equivalent ?? 0)}
          </FigureSlot>{' '}
          a month
          across {activeCount} active{' '}
          {activeCount === 1 ? 'commitment' : 'commitments'}
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
              Payee (optional)
              <input
                type="text"
                className={inputClass}
                value={form.payee}
                onChange={(e) => patch('payee', e.target.value)}
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
              Anchor date (one known due date)
              <input
                type="date"
                className={inputClass}
                value={form.anchor_date}
                onChange={(e) => patch('anchor_date', e.target.value)}
              />
            </label>
            <label className={labelClass}>
              Paid from (optional)
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
            <label className={labelClass}>
              Match pattern (substring of the description or merchant)
              <input
                type="text"
                className={inputClass}
                value={form.match_pattern}
                onChange={(e) => patch('match_pattern', e.target.value)}
              />
            </label>
            <div className="flex gap-3">
              <label className={`${labelClass} flex-1`}>
                Tolerance %
                <input
                  type="number"
                  min="0"
                  step="1"
                  className={inputClass}
                  disabled={form.amount_variable}
                  value={form.amount_tolerance_pct}
                  onChange={(e) => patch('amount_tolerance_pct', e.target.value)}
                />
              </label>
              <label className={`${labelClass} flex-1`}>
                Window (days)
                <input
                  type="number"
                  min="0"
                  step="1"
                  className={inputClass}
                  value={form.match_window_days}
                  onChange={(e) => patch('match_window_days', e.target.value)}
                />
              </label>
            </div>
            <label className="flex items-center gap-2 text-sm text-slate-300">
              <input
                type="checkbox"
                checked={form.amount_variable}
                onChange={(e) => patch('amount_variable', e.target.checked)}
              />
              Amount varies each cycle (skip the amount check)
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
          <div className="flex items-center gap-2">
            <button
              type="button"
              className={primaryBtn}
              disabled={!canSave}
              onClick={save}
            >
              {saving ? 'Saving…' : editing === 'new' ? 'Add bill' : 'Save'}
            </button>
            <button type="button" className={ghostBtn} onClick={close}>
              Cancel
            </button>
          </div>
        </div>
      ) : null}

      {isError ? (
        <p className="text-sm text-coral">Could not load recurring bills.</p>
      ) : isLoading ? (
        <p className="text-sm text-slate-400">Loading bills…</p>
      ) : bills.length === 0 ? (
        <p className="text-sm text-slate-400">
          No recurring bills declared yet.
        </p>
      ) : (
        <ul className="flex flex-col gap-2">
          {bills.map((b) => (
            <li key={b.id} className={rowClass}>
              <div className="flex min-w-0 flex-col gap-0.5">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="truncate font-medium text-white">
                    {b.name}
                  </span>
                  <span className="font-mono text-xs uppercase tracking-wide text-slate-400">
                    {CADENCE_LABELS[b.cadence as Cadence] ?? b.cadence}
                  </span>
                  {b.status !== 'active' ? (
                    <span className="rounded-sm bg-white/5 px-1.5 py-0.5 text-xs text-slate-300">
                      {b.status}
                    </span>
                  ) : null}
                  {b.overdue ? (
                    <span className="rounded-sm bg-coral/15 px-1.5 py-0.5 text-xs font-semibold text-coral">
                      overdue
                    </span>
                  ) : null}
                  {b.amount_variable ? (
                    <span className="rounded-sm bg-sky/10 px-1.5 py-0.5 text-xs text-sky">
                      variable
                    </span>
                  ) : null}
                  {/* No match pattern: nothing will ever link a cycle, so an empty
                      "last paid" says nothing about whether it was paid. */}
                  {!b.auto_matched ? (
                    <span className="rounded-sm bg-white/5 px-1.5 py-0.5 text-xs text-slate-300">
                      by hand
                    </span>
                  ) : null}
                </div>
                <span className="truncate text-xs text-slate-400">
                  Next {formatDate(b.next_due)} · {dueLabel(b.days_until)}
                  {b.account_name ? ` · ${b.account_name}` : ''}
                  {b.payee ? ` · ${b.payee}` : ''}
                </span>
                <span className="truncate text-xs text-slate-400">
                  {b.last_paid_date && b.last_paid_amount != null ? (
                    <>
                      Last paid {formatDate(b.last_paid_date)} ·{' '}
                      {/* The tint says "this cycle did not cost what it was
                          meant to", which is a comparison of two figures that
                          are hidden right now. No magnitude and no sign, but
                          still a read on them, so it goes while censored. */}
                      <span
                        className={
                          !censored &&
                          b.last_paid_amount !== b.expected_amount &&
                          !b.amount_variable
                            ? 'text-citron'
                            : undefined
                        }
                      >
                        {money(b.last_paid_amount)}
                      </span>
                    </>
                  ) : b.auto_matched ? (
                    'No payment matched yet'
                  ) : (
                    'Reconciled by hand, not matched automatically'
                  )}
                </span>
              </div>

              <div className="flex shrink-0 flex-col items-end gap-1.5">
                <span className="text-sm font-semibold tabular-nums text-white">
                  {money(b.expected_amount)}
                </span>
                <div className="flex items-center gap-1.5">
                  <button
                    type="button"
                    className={editBtn}
                    disabled={listBusy}
                    onClick={() => openEdit(b)}
                  >
                    Edit
                  </button>
                  <button
                    type="button"
                    className={rowGhostBtn}
                    disabled={listBusy}
                    onClick={() => togglePause(b)}
                  >
                    {b.status === 'paused' ? 'Resume' : 'Pause'}
                  </button>
                  <button
                    type="button"
                    className={dangerBtn}
                    disabled={listBusy}
                    onClick={() => del(b)}
                  >
                    Delete
                  </button>
                </div>
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function BillGlyph() {
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
      <path d="M7 3h10a2 2 0 0 1 2 2v16l-3-2-2 2-2-2-2 2-2-2-3 2V5a2 2 0 0 1 2-2Z" />
      <path d="M9 8h6M9 12h6" />
    </svg>
  );
}
