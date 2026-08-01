'use client';

// Accounts list for the finance dashboard: one row per account with a type
// badge, masked number and its latest snapshot balance. For liability accounts
// (class=liability) the snapshot balance is negative = owed, so we show the
// magnitude in coral labelled "owed"; assets show their balance plainly.
//
// Each row also carries the two owner-authored fields (portfolio-site#122): a
// free-text description of what the account is actually for, and a drawdown
// policy saying whether the balance is spendable. Neither can be derived from
// the bank, so this inline editor is the only place they get set. Everything
// else on the row is ingest-owned and read-only here.

import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';

import {
  useListFinanceAccounts,
  useUpdateAccount,
  getListFinanceAccountsQueryKey,
} from '@/lib/api/generated';
import type { FinanceAccountDTO } from '@/lib/api/model';
import {
  citronCard,
  citronBadge,
  inputClass,
  labelClass,
  selectClass,
  primaryBtn,
  ghostBtn,
  editBtn,
  rowClass,
} from '@/components/admin/ui';
import { formatDate, accountTypeLabel } from './format';
import { FigureSlot, useMoney } from './censor';

type DrawdownPolicy = FinanceAccountDTO['drawdown_policy'];

// Mirrors the schema's MaxLen(2000), which the backend measures in BYTES (see
// descriptionMaxBytes in backend/internal/api/admin_accounts.go).
const DESCRIPTION_MAX_BYTES = 2000;

// Order runs least to most restricted, so the select reads as a scale.
const POLICIES: DrawdownPolicy[] = [
  'unset',
  'flexible',
  'no_drawdown',
  'emergency_only',
];

const POLICY_LABELS: Record<DrawdownPolicy, string> = {
  unset: 'Not declared',
  flexible: 'Flexible',
  no_drawdown: 'No drawdown',
  emergency_only: 'Emergency only',
};

export function AccountsSection() {
  const queryClient = useQueryClient();
  const { data, isLoading, isError } = useListFinanceAccounts();
  const accounts = data?.accounts ?? [];

  // The id of the account being edited, or null when the list is just a list.
  const [editing, setEditing] = useState<number | null>(null);
  const update = useUpdateAccount();

  // One mutation drives every row, and React Query holds onto the last error until the
  // next call. Reset on both open and cancel so a failed save cannot leave a stale
  // banner hanging over a different account, or over the same one after a retry.
  const openEdit = (id: number) => {
    update.reset();
    setEditing(id);
  };
  const cancelEdit = () => {
    update.reset();
    setEditing(null);
  };

  const save = (id: number, description: string, policy: DrawdownPolicy) =>
    update.mutate(
      { id, data: { description, drawdown_policy: policy } },
      {
        onSuccess: () => {
          queryClient.invalidateQueries({
            queryKey: getListFinanceAccountsQueryKey(),
          });
          // Guarded, so an in-flight save that lands after the user has opened a
          // different row does not close that row's editor underneath them.
          setEditing((cur) => (cur === id ? null : cur));
        },
      },
    );

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
          {accounts.map((acct) =>
            editing === acct.id ? (
              <li
                key={acct.id}
                className="flex flex-col gap-3 rounded-lg border border-slate-700 bg-deepsea/40 p-3"
              >
                <AccountEditor
                  account={acct}
                  saving={update.isPending}
                  failed={update.error != null}
                  onCancel={cancelEdit}
                  onSave={(description, policy) =>
                    save(acct.id, description, policy)
                  }
                />
              </li>
            ) : (
              <li key={acct.id} className={rowClass}>
                <AccountRow account={acct} onEdit={() => openEdit(acct.id)} />
              </li>
            ),
          )}
        </ul>
      )}
    </section>
  );
}

function AccountRow({
  account,
  onEdit,
}: {
  account: FinanceAccountDTO;
  onEdit: () => void;
}) {
  const { money, abs } = useMoney();
  // Coral + "owed" comes from account.class, not from the sign of the hidden
  // number, so the tint stays on while censored without leaking anything.
  const isLiability = account.class === 'liability';

  return (
    <>
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate font-medium text-white">{account.name}</span>
          <TypeBadge type={account.type} />
          {account.drawdown_policy !== 'unset' && (
            <PolicyBadge policy={account.drawdown_policy} />
          )}
        </div>
        <span className="font-mono text-xs text-slate-400">
          {account.masked_number || '—'}
        </span>
        {account.description && (
          <p className="max-w-prose text-xs leading-relaxed text-slate-300">
            {account.description}
          </p>
        )}
        {(account.available != null || account.credit_limit != null) && (
          <span className="text-xs text-slate-400">
            {account.available != null && (
              <>
                Available <FigureSlot>{money(account.available)}</FigureSlot>
              </>
            )}
            {account.available != null && account.credit_limit != null && ' · '}
            {account.credit_limit != null && (
              <>
                Limit <FigureSlot>{money(account.credit_limit)}</FigureSlot>
              </>
            )}
          </span>
        )}
      </div>

      <div className="flex shrink-0 flex-col items-end gap-1">
        {account.balance == null ? (
          <span className="text-sm text-slate-400">no snapshot</span>
        ) : isLiability ? (
          <span className="font-display text-lg font-semibold tabular-nums text-coral">
            {abs(account.balance)}
            <span className="ml-1 text-xs font-normal text-slate-400">owed</span>
          </span>
        ) : (
          <span className="font-display text-lg font-semibold tabular-nums text-white">
            {money(account.balance)}
          </span>
        )}
        {account.balance_as_of && (
          <span className="text-[0.65rem] text-slate-400">
            {formatDate(account.balance_as_of)}
          </span>
        )}
        <button type="button" className={editBtn} onClick={onEdit}>
          Edit
        </button>
      </div>
    </>
  );
}

// AccountEditor holds its own draft state so typing never re-renders the list, and
// so cancelling discards cleanly. Only the two owner-authored fields are editable:
// name, type, class and the balance belong to the ingest.
function AccountEditor({
  account,
  saving,
  failed,
  onSave,
  onCancel,
}: {
  account: FinanceAccountDTO;
  saving: boolean;
  failed: boolean;
  onSave: (description: string, policy: DrawdownPolicy) => void;
  onCancel: () => void;
}) {
  const [description, setDescription] = useState(account.description);
  const [policy, setPolicy] = useState<DrawdownPolicy>(
    account.drawdown_policy,
  );

  // The API's limit is 2000 BYTES, so a textarea maxLength (which counts UTF-16 code
  // units) would let an accented or emoji note compose a request the API then rejects.
  // Measure what the server measures, and refuse to send rather than round-trip a 422.
  const bytes = new TextEncoder().encode(description).length;
  const overBy = bytes - DESCRIPTION_MAX_BYTES;
  const tooLong = overBy > 0;

  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <span className="truncate font-medium text-white">{account.name}</span>
        <TypeBadge type={account.type} />
      </div>

      <label className={labelClass}>
        What this account is for
        <textarea
          rows={3}
          className={inputClass}
          placeholder="In your own words. The MCP reads this before treating a balance as spendable."
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </label>

      {/* Quiet until it matters: the count appears only near the limit. */}
      {bytes > DESCRIPTION_MAX_BYTES - 200 && (
        <p className={`text-xs ${tooLong ? 'text-coral' : 'text-slate-400'}`}>
          {tooLong
            ? `Too long by ${overBy} of ${DESCRIPTION_MAX_BYTES} characters. Accents and emoji count as more than one.`
            : `${bytes} of ${DESCRIPTION_MAX_BYTES} characters.`}
        </p>
      )}

      {failed && (
        <p className="text-sm text-coral">
          Could not save this account. Nothing was changed.
        </p>
      )}

      <label className={labelClass}>
        Drawdown policy
        <select
          className={selectClass}
          value={policy}
          onChange={(e) => setPolicy(e.target.value as DrawdownPolicy)}
        >
          {POLICIES.map((p) => (
            <option key={p} value={p}>
              {POLICY_LABELS[p]}
            </option>
          ))}
        </select>
      </label>

      <div className="flex gap-2">
        <button
          type="button"
          className={primaryBtn}
          disabled={saving || tooLong}
          onClick={() => onSave(description, policy)}
        >
          {saving ? 'Saving…' : 'Save'}
        </button>
        <button
          type="button"
          className={ghostBtn}
          disabled={saving}
          onClick={onCancel}
        >
          Cancel
        </button>
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

// A declared policy is worth calling out on the row; "unset" is not rendered, since
// an absent badge already says "not declared" without adding noise to every account.
function PolicyBadge({ policy }: { policy: DrawdownPolicy }) {
  const locked = policy !== 'flexible';
  return (
    <span
      className="rounded-full px-2 py-0.5 text-[0.65rem] font-medium uppercase tracking-wide"
      style={{
        color: locked ? 'var(--color-coral)' : 'var(--color-mint)',
        background: `color-mix(in srgb, ${
          locked ? 'var(--color-coral)' : 'var(--color-mint)'
        } 14%, transparent)`,
      }}
    >
      {POLICY_LABELS[policy]}
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
