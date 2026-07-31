'use client';

// Wishlist section of the Admin Console (portfolio-site#123): the one-off things
// Alif wants to buy or pay for once. Writes go to /api/admin/wishlist behind the
// server-enforced admin middleware; the admin gate on this UI is UX only. The list
// reads the same admin endpoint the dashboard and the list_wishlist MCP tool read
// (GET /api/finance/wishlist), so the numbers here cannot drift from the ones a
// model sees.
//
// The lifecycle is the server's: switching status to bought or abandoned stamps
// resolved_at, switching back to wanted clears it. Nothing here computes that.
//
// The image reuses the presigned direct-to-S3 upload (kind=wishlist) so the file
// bytes bypass the t4g.micro, exactly like ProjectsSection.

import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';

import {
  useListFinanceWishlist,
  useCreateWishlistItem,
  useUpdateWishlistItem,
  useDeleteWishlistItem,
  useCreateUploadPresign,
  getListFinanceWishlistQueryKey,
} from '@/lib/api/generated';
import type {
  FinanceWishlistItemDTO,
  FinanceWishlistItemDTOPriority,
  FinanceWishlistItemDTOStatus,
  ListFinanceWishlistStatus,
} from '@/lib/api/model';
import {
  PresignUploadInputBodyContentType,
  PresignUploadInputBodyKind,
} from '@/lib/api/model';
import {
  citronCard,
  citronBadge,
  inputClass,
  labelClass,
  selectClass,
  primaryBtn,
  ghostBtn,
  rowClass,
  editBtn,
  dangerBtn,
} from './ui';

interface WishlistForm {
  name: string;
  description: string;
  amount: string; // blank means the price is unknown, which is not the same as free
  amount_is_estimate: boolean;
  currency: string;
  priority: FinanceWishlistItemDTOPriority;
  status: FinanceWishlistItemDTOStatus;
  deadline: string; // YYYY-MM-DD, blank for no date
  link: string;
  image_key: string;
}

const emptyForm: WishlistForm = {
  name: '',
  description: '',
  amount: '',
  amount_is_estimate: true,
  currency: 'AUD',
  priority: 'medium',
  status: 'wanted',
  deadline: '',
  link: '',
  image_key: '',
};

function itemToForm(w: FinanceWishlistItemDTO): WishlistForm {
  return {
    name: w.name,
    description: w.description,
    amount: w.amount === null ? '' : String(w.amount),
    amount_is_estimate: w.amount_is_estimate,
    currency: w.currency || 'AUD',
    priority: w.priority,
    status: w.status,
    deadline: w.deadline ?? '',
    link: w.link,
    image_key: w.image_key,
  };
}

const priorities: FinanceWishlistItemDTOPriority[] = ['high', 'medium', 'low'];
const statuses: FinanceWishlistItemDTOStatus[] = [
  'wanted',
  'bought',
  'abandoned',
];
const filters: ListFinanceWishlistStatus[] = [
  'all',
  'wanted',
  'bought',
  'abandoned',
];

// A short fixed list rather than a free-text box: the cost roll-up is a
// single-currency figure (AUD), so a typo'd or exotic code would silently drop an
// item out of the total. Anything outside AUD is reported separately by the read
// layer as currency_mismatch_count.
const currencies = ['AUD', 'USD', 'EUR', 'GBP', 'JPY', 'SGD', 'IDR'];

const allowedTypes = Object.values(PresignUploadInputBodyContentType) as string[];

// Money is formatted here, never on the server (the read layer does not round).
function formatAmount(amount: number | null, currency: string): string {
  if (amount === null) return 'price unknown';
  return `${currency} ${amount.toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  })}`;
}

export function WishlistSection() {
  const queryClient = useQueryClient();
  // Defaults to every status: this is the management view, so nothing should be
  // hidden here. The read endpoint's own default (wanted only) still governs the
  // dashboard and the MCP tool.
  const [filter, setFilter] = useState<ListFinanceWishlistStatus>('all');
  const { data, isLoading } = useListFinanceWishlist({ status: filter });

  const items = data?.items ?? [];
  const totals = data?.totals;

  // null = closed, 'new' = create form, number = editing that item id.
  const [editing, setEditing] = useState<number | 'new' | null>(null);
  const [form, setForm] = useState<WishlistForm>(emptyForm);
  const [uploadError, setUploadError] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);

  const invalidate = () =>
    queryClient.invalidateQueries({
      queryKey: getListFinanceWishlistQueryKey(),
      exact: false,
    });

  const create = useCreateWishlistItem();
  const update = useUpdateWishlistItem();
  const remove = useDeleteWishlistItem();
  const presign = useCreateUploadPresign();

  const saving = create.isPending || update.isPending;

  const openNew = () => {
    setForm(emptyForm);
    setUploadError(null);
    setEditing('new');
  };
  const openEdit = (w: FinanceWishlistItemDTO) => {
    setForm(itemToForm(w));
    setUploadError(null);
    setEditing(w.id);
  };
  const close = () => {
    setEditing(null);
    setUploadError(null);
  };

  const patch = <K extends keyof WishlistForm>(key: K, value: WishlistForm[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  // A row already carrying a code outside the list keeps it as an option, so editing
  // an unrelated field never silently rewrites its currency.
  const currencyOptions = currencies.includes(form.currency)
    ? currencies
    : [...currencies, form.currency];

  // A blank amount is the "price unknown" case and is valid; anything typed has to be
  // a positive cost, which the server also enforces with a 422.
  const amountRaw = form.amount.trim();
  const amountValue = amountRaw === '' ? null : Number.parseFloat(amountRaw);
  const amountInvalid =
    amountValue !== null && (!Number.isFinite(amountValue) || amountValue <= 0);

  const canSave = form.name.trim().length > 0 && !amountInvalid && !saving;

  const save = () => {
    if (!canSave) return;
    const amount = amountValue;
    const onSuccess = () => {
      invalidate();
      close();
    };
    const shared = {
      name: form.name.trim(),
      description: form.description,
      amount_is_estimate: form.amount_is_estimate,
      currency: form.currency.trim() || 'AUD',
      priority: form.priority,
      status: form.status,
      deadline: form.deadline,
      link: form.link.trim(),
      image_key: form.image_key,
    };
    if (editing === 'new') {
      create.mutate(
        { data: amount === null ? shared : { ...shared, amount } },
        { onSuccess },
      );
    } else if (typeof editing === 'number') {
      // A blank amount on edit has to say "unknown" explicitly: an omitted key
      // leaves the column alone, so it could never clear a price.
      update.mutate(
        {
          id: editing,
          data:
            amount === null
              ? { ...shared, amount_unknown: true }
              : { ...shared, amount },
        },
        { onSuccess },
      );
    }
  };

  const del = (w: FinanceWishlistItemDTO) => {
    if (
      !confirm(
        `Delete "${w.name}"? Use bought or abandoned instead if you want to keep the history.`,
      )
    )
      return;
    remove.mutate({ id: w.id }, { onSuccess: invalidate });
  };

  // Presigned upload: ask the backend for a PUT URL under the wishlist/ prefix,
  // then PUT the raw File to S3. One image per item, so a new upload replaces it.
  const handleFile = (file: File) => {
    setUploadError(null);
    if (!allowedTypes.includes(file.type)) {
      setUploadError(
        `Unsupported type "${file.type || 'unknown'}". Use PNG, JPEG, WebP or GIF.`,
      );
      return;
    }
    const content_type = file.type as PresignUploadInputBodyContentType;
    setUploading(true);
    presign.mutate(
      { data: { content_type, kind: PresignUploadInputBodyKind.wishlist } },
      {
        onSuccess: (out) => {
          void (async () => {
            try {
              const res = await fetch(out.url, {
                method: out.method,
                headers: out.headers,
                body: file,
              });
              if (!res.ok) {
                throw new Error(`S3 responded ${res.status}`);
              }
              setForm((f) => ({ ...f, image_key: out.key }));
            } catch {
              setUploadError(
                'Upload failed. The rest of the form still saves.',
              );
            } finally {
              setUploading(false);
            }
          })();
        },
        onError: (err) => {
          setUploading(false);
          setUploadError((err as Error).message);
        },
      },
    );
  };

  return (
    <section
      className="flex flex-col gap-5 rounded-2xl border p-5 sm:p-6"
      style={citronCard}
    >
      <header className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <span
            className="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-xl text-citron"
            style={citronBadge}
          >
            <WishlistGlyph />
          </span>
          <div>
            <h2 className="font-display text-lg font-bold text-white">
              Wishlist
            </h2>
            <p className="text-sm text-slate-400">
              One-off things to buy or pay for once. Bought and abandoned items
              stay for context.
            </p>
          </div>
        </div>
        {editing === null && (
          <button
            type="button"
            onClick={openNew}
            className="shrink-0 rounded-lg border border-slate-700 px-3 py-1.5 text-sm font-medium text-white transition hover:border-citron hover:text-citron"
          >
            New item
          </button>
        )}
      </header>

      {editing !== null && (
        <div className="flex flex-col gap-4 rounded-xl border border-slate-700 bg-deepsea/40 p-4">
          <label className={labelClass}>
            Name
            <input
              className={inputClass}
              maxLength={200}
              placeholder="new glasses"
              value={form.name}
              onChange={(e) => patch('name', e.target.value)}
            />
          </label>

          <label className={labelClass}>
            Notes
            <textarea
              className={inputClass}
              rows={3}
              placeholder="Why, which model, what was ruled out"
              value={form.description}
              onChange={(e) => patch('description', e.target.value)}
            />
          </label>

          <div className="grid gap-3 sm:grid-cols-2">
            <label className={labelClass}>
              Amount (blank = price unknown)
              <input
                className={inputClass}
                type="number"
                min="0"
                step="0.01"
                placeholder="unknown"
                value={form.amount}
                onChange={(e) => patch('amount', e.target.value)}
              />
              {amountInvalid ? (
                <span className="text-xs text-coral">
                  Enter a positive cost, or leave it blank for an unknown price.
                </span>
              ) : null}
            </label>
            <label className={labelClass}>
              Currency
              <select
                className={selectClass}
                value={form.currency}
                onChange={(e) => patch('currency', e.target.value)}
              >
                {currencyOptions.map((c) => (
                  <option key={c} value={c}>
                    {c}
                  </option>
                ))}
              </select>
            </label>
          </div>

          <label className="flex items-center gap-2 text-sm text-slate-300">
            <input
              type="checkbox"
              className="h-4 w-4 accent-[var(--color-citron)]"
              checked={form.amount_is_estimate}
              onChange={(e) => patch('amount_is_estimate', e.target.checked)}
            />
            Amount is an estimate, not a quoted price
          </label>

          <div className="grid gap-3 sm:grid-cols-3">
            <label className={labelClass}>
              Priority
              <select
                className={selectClass}
                value={form.priority}
                onChange={(e) =>
                  patch(
                    'priority',
                    e.target.value as FinanceWishlistItemDTOPriority,
                  )
                }
              >
                {priorities.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            </label>
            <label className={labelClass}>
              Status
              <select
                className={selectClass}
                value={form.status}
                onChange={(e) =>
                  patch('status', e.target.value as FinanceWishlistItemDTOStatus)
                }
              >
                {statuses.map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </select>
            </label>
            <label className={labelClass}>
              Deadline (optional)
              <input
                className={inputClass}
                type="date"
                value={form.deadline}
                onChange={(e) => patch('deadline', e.target.value)}
              />
            </label>
          </div>

          <label className={labelClass}>
            Link (optional)
            <input
              className={inputClass}
              inputMode="url"
              placeholder="https://…"
              value={form.link}
              onChange={(e) => patch('link', e.target.value)}
            />
          </label>

          {/* Image: one presigned direct-to-S3 upload per item. */}
          <div className="flex flex-col gap-2">
            <span className="text-sm text-slate-300">Image</span>
            {form.image_key ? (
              <div className="flex items-center gap-2 self-start rounded-md border border-slate-700 bg-deepsea px-2.5 py-1 text-xs text-slate-300">
                <span className="max-w-[16rem] truncate font-mono">
                  {form.image_key}
                </span>
                <button
                  type="button"
                  aria-label="Remove image"
                  onClick={() => patch('image_key', '')}
                  className="text-coral transition hover:brightness-110"
                >
                  ✕
                </button>
              </div>
            ) : (
              <p className="text-xs text-slate-400">No image attached.</p>
            )}
            <label className="flex w-fit cursor-pointer items-center gap-2 rounded-lg border border-slate-700 px-3 py-1.5 text-sm text-white transition hover:border-citron hover:text-citron">
              {uploading ? 'Uploading…' : 'Upload image'}
              <input
                type="file"
                accept="image/png,image/jpeg,image/webp,image/gif"
                className="hidden"
                disabled={uploading}
                onChange={(e) => {
                  const file = e.target.files?.[0];
                  if (file) handleFile(file);
                  e.target.value = '';
                }}
              />
            </label>
            {uploadError ? (
              <p className="text-sm text-coral">{uploadError}</p>
            ) : null}
          </div>

          {(create.error || update.error) && (
            <p className="text-sm text-coral">
              {((create.error || update.error) as Error).message}
            </p>
          )}

          <div className="flex gap-2">
            <button
              type="button"
              className={primaryBtn}
              disabled={!canSave}
              onClick={save}
            >
              {saving ? 'Saving…' : editing === 'new' ? 'Create' : 'Save'}
            </button>
            <button type="button" className={ghostBtn} onClick={close}>
              Cancel
            </button>
          </div>
        </div>
      )}

      {/* Filter + roll-up. The totals always describe the rows shown. */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <label className="flex items-center gap-2 text-sm text-slate-300">
          Show
          <select
            className={`${selectClass} w-auto`}
            value={filter}
            onChange={(e) =>
              setFilter(e.target.value as ListFinanceWishlistStatus)
            }
          >
            {filters.map((f) => (
              <option key={f} value={f}>
                {f}
              </option>
            ))}
          </select>
        </label>
        {totals ? (
          <p className="text-sm text-slate-400">
            {totals.item_count} item{totals.item_count === 1 ? '' : 's'} ·{' '}
            {formatAmount(totals.known_cost_total, totals.currency)} known
            {totals.unknown_cost_count > 0
              ? ` · ${totals.unknown_cost_count} with no price`
              : ''}
            {totals.currency_mismatch_count > 0
              ? ` · ${totals.currency_mismatch_count} in another currency, not in the total`
              : ''}
          </p>
        ) : null}
      </div>

      {data?.truncated ? (
        <p className="text-sm text-coral">
          The list is longer than the read limit, so the lowest-priority items and
          their cost are missing from this view.
        </p>
      ) : null}

      {isLoading ? (
        <p className="text-sm text-slate-400">Loading…</p>
      ) : items.length === 0 ? (
        <p className="text-sm text-slate-400">Nothing on the list.</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {items.map((w) => (
            <li key={w.id} className={rowClass}>
              <div className="min-w-0">
                <p className="flex flex-wrap items-center gap-2 font-medium text-white">
                  <span className="truncate">{w.name}</span>
                  <span className="shrink-0 rounded-full bg-citron/20 px-2 py-0.5 font-mono text-[0.6rem] uppercase tracking-widest text-citron">
                    {w.priority}
                  </span>
                  {w.status !== 'wanted' ? (
                    <span className="shrink-0 rounded-full bg-white/10 px-2 py-0.5 font-mono text-[0.6rem] uppercase tracking-widest text-slate-300">
                      {w.status}
                    </span>
                  ) : null}
                </p>
                <p className="mt-0.5 truncate text-sm text-slate-400">
                  {formatAmount(w.amount, w.currency)}
                  {w.amount !== null && w.amount_is_estimate ? ' (est.)' : ''}
                  {w.deadline ? ` · by ${w.deadline}` : ''}
                  {w.description ? ` · ${w.description}` : ''}
                </p>
                {w.link ? (
                  <a
                    href={w.link}
                    target="_blank"
                    rel="noreferrer noopener"
                    className="mt-0.5 inline-block max-w-full truncate text-xs text-sky hover:underline"
                  >
                    {w.link}
                  </a>
                ) : null}
              </div>
              <div className="flex shrink-0 items-center gap-1.5">
                <button
                  type="button"
                  onClick={() => openEdit(w)}
                  className={editBtn}
                >
                  Edit
                </button>
                <button
                  type="button"
                  onClick={() => del(w)}
                  disabled={remove.isPending}
                  className={dangerBtn}
                >
                  Delete
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function WishlistGlyph() {
  return (
    <svg
      width="18"
      height="18"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M4 6h13l-1.2 7.5a2 2 0 0 1-2 1.7H8.2a2 2 0 0 1-2-1.7L5 4H2" />
      <circle cx="9" cy="19.5" r="1.3" />
      <circle cx="16" cy="19.5" r="1.3" />
    </svg>
  );
}
