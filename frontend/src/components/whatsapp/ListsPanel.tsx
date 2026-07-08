'use client';

import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';

import {
  useListWaLists,
  useCreateWaList,
  useUpdateWaList,
  useDeleteWaList,
  getWaList,
  getListWaListsQueryKey,
} from '@/lib/api/generated';
import type { ListDTO, LineError, RecipientDTO } from '@/lib/api/model';
import { useAuth } from '@/lib/auth';
import { WA_COUNTRIES, dialLabel } from '@/lib/wa-countries';

const inputClass =
  'w-full rounded-lg border border-slate-700 bg-deepsea px-3 py-2 text-white outline-none focus:border-sky';

// citron-accented card. Pairs with the citron CountryCodeSetting above it, so
// the "lists + their default country code" cluster reads as one group.
const cardStyle = {
  borderColor: 'color-mix(in srgb, var(--color-citron) 42%, transparent)',
  background: 'color-mix(in srgb, var(--color-citron) 8%, var(--color-deepsea))',
};
const badgeStyle = {
  background: 'color-mix(in srgb, var(--color-citron) 18%, transparent)',
};

// recipientsToText renders stored recipients back into the paste format so an
// edit starts from the current membership.
function recipientsToText(recipients: RecipientDTO[]): string {
  return recipients
    .map((r) => (r.name ? `${r.phone}, ${r.name}` : r.phone))
    .join('\n');
}

export function ListsPanel() {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const userDefault = user?.default_country_code ?? '';
  const { data, isLoading } = useListWaLists();
  const lists = data?.lists ?? [];

  const [editing, setEditing] = useState<number | 'new' | null>(null);
  const [form, setForm] = useState({ name: '', recipients_text: '', country_code: '' });
  const [invalidLines, setInvalidLines] = useState<LineError[]>([]);

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: getListWaListsQueryKey() });

  const create = useCreateWaList();
  const update = useUpdateWaList();
  const remove = useDeleteWaList();
  const busy = create.isPending || update.isPending || remove.isPending;
  const error = (create.error || update.error || remove.error) as Error | null;

  const openNew = () => {
    setForm({ name: '', recipients_text: '', country_code: '' });
    setInvalidLines([]);
    setEditing('new');
  };

  const openEdit = async (l: ListDTO) => {
    setInvalidLines([]);
    setForm({ name: l.name, recipients_text: '', country_code: l.country_code ?? '' });
    setEditing(l.id);
    // Load the current members so editing starts from them, not a blank paste
    // (PUT replaces the whole membership).
    try {
      const full = await getWaList(l.id);
      setForm({
        name: full.list.name,
        recipients_text: recipientsToText(full.list.recipients ?? []),
        country_code: full.list.country_code ?? '',
      });
    } catch {
      // Leave the name in place; the user can re-paste recipients.
    }
  };

  const close = () => {
    setEditing(null);
    setInvalidLines([]);
  };

  const save = () => {
    const onSuccess = (resp: { invalid_lines?: LineError[] | null }) => {
      invalidate();
      const invalid = resp.invalid_lines ?? [];
      setInvalidLines(invalid);
      // Keep the editor open when some lines need fixing; otherwise close.
      if (invalid.length === 0) close();
    };
    if (editing === 'new') {
      create.mutate({ data: form }, { onSuccess });
    } else if (typeof editing === 'number') {
      update.mutate({ id: editing, data: form }, { onSuccess });
    }
  };

  const del = (id: number) => {
    if (!confirm('Delete this list?')) return;
    remove.mutate({ id }, { onSuccess: invalidate });
  };

  const canSave = form.name.trim().length > 0;
  // The code that a leading 0 will actually become: the per-list override if
  // set, otherwise the user's default.
  const effectiveCode = form.country_code || userDefault;

  return (
    <section
      className="flex flex-col gap-4 rounded-2xl border p-5 sm:p-6"
      style={cardStyle}
    >
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <span
            className="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-xl text-citron"
            style={badgeStyle}
          >
            <ListsGlyph />
          </span>
          <h2 className="font-display text-lg font-bold text-white">
            Recipient lists
          </h2>
        </div>
        {editing === null && (
          <button
            type="button"
            onClick={openNew}
            className="rounded-lg border border-slate-700 px-3 py-1.5 text-sm font-medium text-white transition hover:border-citron hover:text-citron"
          >
            New list
          </button>
        )}
      </div>

      {editing !== null && (
        <div className="flex flex-col gap-3">
          <input
            className={inputClass}
            placeholder="List name"
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
          />
          <label className="flex flex-col gap-1 text-sm text-slate-300">
            Country code for local (0…) numbers
            <select
              className={inputClass}
              value={form.country_code}
              onChange={(e) => setForm({ ...form, country_code: e.target.value })}
            >
              <option value="">
                Use my default{userDefault ? ` (+${userDefault})` : ''}
              </option>
              {WA_COUNTRIES.map((c) => (
                <option key={c.code} value={c.code}>
                  {c.name} (+{c.code})
                </option>
              ))}
            </select>
          </label>
          <textarea
            className={`${inputClass} font-mono text-sm`}
            rows={7}
            placeholder={'Paste numbers, one per line:\n0412345678, Budi\n61498765432, Siti\n0400000000'}
            value={form.recipients_text}
            onChange={(e) => setForm({ ...form, recipients_text: e.target.value })}
          />
          <p className="text-xs text-slate-400">
            One recipient per line: <code className="text-sky">number</code> or{' '}
            <code className="text-sky">number, name</code>.{' '}
            {effectiveCode
              ? `Numbers starting with 0 will use ${dialLabel(effectiveCode)}; other countries need their code.`
              : 'Numbers need their country code.'}{' '}
            Max 250.
          </p>

          {invalidLines.length > 0 && (
            <div className="rounded-lg border border-coral/50 bg-coral/10 p-3 text-sm">
              <p className="font-medium text-coral">
                {invalidLines.length} line{invalidLines.length === 1 ? '' : 's'}{' '}
                could not be read (the rest were saved):
              </p>
              <ul className="mt-1 list-inside list-disc text-slate-300">
                {invalidLines.slice(0, 10).map((l) => (
                  <li key={l.line}>
                    line {l.line}: <span className="text-slate-400">{l.raw}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}

          {error && (
            <p className="text-sm text-coral">{error.message}</p>
          )}

          <div className="flex gap-2">
            <button
              type="button"
              disabled={!canSave || busy}
              onClick={save}
              className="rounded-lg bg-citron px-4 py-1.5 text-sm font-semibold text-ink transition hover:brightness-95 disabled:opacity-50"
            >
              {busy ? 'Saving…' : 'Save'}
            </button>
            <button
              type="button"
              onClick={close}
              className="rounded-lg border border-slate-700 px-4 py-1.5 text-sm text-white transition hover:border-slate-500"
            >
              {invalidLines.length > 0 ? 'Done' : 'Cancel'}
            </button>
          </div>
        </div>
      )}

      {isLoading ? (
        <p className="text-sm text-slate-400">Loading…</p>
      ) : lists.length === 0 ? (
        <p className="text-sm text-slate-400">No lists yet.</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {lists.map((l) => (
            <li
              key={l.id}
              className="flex items-center justify-between gap-4 rounded-lg border border-slate-700 bg-deepsea/40 p-3"
            >
              <div className="min-w-0">
                <p className="font-medium text-white">{l.name}</p>
                <p className="mt-0.5 text-sm text-slate-400">
                  {l.recipient_count} recipient{l.recipient_count === 1 ? '' : 's'}
                </p>
              </div>
              <div className="flex shrink-0 gap-3 text-sm">
                <button
                  type="button"
                  onClick={() => openEdit(l)}
                  className="text-sky transition hover:brightness-110"
                >
                  Edit
                </button>
                <button
                  type="button"
                  onClick={() => del(l.id)}
                  className="text-coral transition hover:brightness-110"
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

// A list glyph (rows with bullets) in the house line-drawing style.
function ListsGlyph() {
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
      <path d="M9 6h11M9 12h11M9 18h11" />
      <circle cx="4.5" cy="6" r="1.1" fill="currentColor" stroke="none" />
      <circle cx="4.5" cy="12" r="1.1" fill="currentColor" stroke="none" />
      <circle cx="4.5" cy="18" r="1.1" fill="currentColor" stroke="none" />
    </svg>
  );
}
