'use client';

import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';

import {
  useListWaTemplates,
  useCreateWaTemplate,
  useUpdateWaTemplate,
  useDeleteWaTemplate,
  getListWaTemplatesQueryKey,
} from '@/lib/api/generated';
import type { TemplateDTO } from '@/lib/api/model';
import { editBtn, dangerBtn } from './ui';

const inputClass =
  'w-full rounded-lg border border-slate-700 bg-deepsea px-3 py-2 text-white outline-none focus:border-sky';

// sky-accented card (message content / templates).
const cardStyle = {
  borderColor: 'color-mix(in srgb, var(--color-sky) 40%, transparent)',
  background: 'color-mix(in srgb, var(--color-sky) 7%, var(--color-deepsea))',
};
const badgeStyle = {
  background: 'color-mix(in srgb, var(--color-sky) 16%, transparent)',
};

export function TemplatesPanel() {
  const queryClient = useQueryClient();
  const { data, isLoading } = useListWaTemplates();
  const templates = data?.templates ?? [];

  // `editing` is null (closed), 'new', or the id of the template being edited.
  const [editing, setEditing] = useState<number | 'new' | null>(null);
  const [form, setForm] = useState({ name: '', body: '' });

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: getListWaTemplatesQueryKey() });

  const create = useCreateWaTemplate();
  const update = useUpdateWaTemplate();
  const remove = useDeleteWaTemplate();
  const busy = create.isPending || update.isPending || remove.isPending;

  const openNew = () => {
    setForm({ name: '', body: '' });
    setEditing('new');
  };
  const openEdit = (t: TemplateDTO) => {
    setForm({ name: t.name, body: t.body });
    setEditing(t.id);
  };
  const close = () => setEditing(null);

  const save = () => {
    const onSuccess = () => {
      invalidate();
      close();
    };
    if (editing === 'new') {
      create.mutate({ data: form }, { onSuccess });
    } else if (typeof editing === 'number') {
      update.mutate({ id: editing, data: form }, { onSuccess });
    }
  };

  const del = (id: number) => {
    if (!confirm('Delete this template?')) return;
    remove.mutate({ id }, { onSuccess: invalidate });
  };

  const canSave = form.name.trim().length > 0 && form.body.trim().length > 0;

  return (
    <section
      className="flex flex-col gap-4 rounded-2xl border p-5 sm:p-6"
      style={cardStyle}
    >
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <span
            className="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-xl text-sky"
            style={badgeStyle}
          >
            <TemplatesGlyph />
          </span>
          <h2 className="font-display text-lg font-bold text-white">Templates</h2>
        </div>
        {editing === null && (
          <button
            type="button"
            onClick={openNew}
            className="rounded-lg border border-slate-700 px-3 py-1.5 text-sm font-medium text-white transition hover:border-sky hover:text-sky"
          >
            New template
          </button>
        )}
      </div>

      {editing !== null && (
        <div className="flex flex-col gap-3">
          <input
            className={inputClass}
            placeholder="Template name"
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
          />
          <textarea
            className={inputClass}
            rows={5}
            placeholder="Message body. Use {name} to personalize."
            value={form.body}
            onChange={(e) => setForm({ ...form, body: e.target.value })}
          />
          <p className="text-xs text-slate-400">
            <code className="text-sky">{'{name}'}</code> is replaced with each
            recipient&apos;s name (blank if they have none).
          </p>
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
              Cancel
            </button>
          </div>
        </div>
      )}

      {isLoading ? (
        <p className="text-sm text-slate-400">Loading…</p>
      ) : templates.length === 0 ? (
        <p className="text-sm text-slate-400">No templates yet.</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {templates.map((t) => (
            <li
              key={t.id}
              className="flex items-start justify-between gap-4 rounded-lg border border-slate-700 bg-deepsea/40 p-3"
            >
              <div className="min-w-0">
                <p className="font-medium text-white">{t.name}</p>
                <p className="mt-0.5 line-clamp-2 whitespace-pre-wrap text-sm text-slate-400">
                  {t.body}
                </p>
              </div>
              <div className="flex shrink-0 items-start gap-2">
                <button type="button" onClick={() => openEdit(t)} className={editBtn}>
                  Edit
                </button>
                <button
                  type="button"
                  onClick={() => del(t.id)}
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

// Document glyph in the house line-drawing style, tinted via currentColor.
function TemplatesGlyph() {
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
      <rect x="5" y="3" width="14" height="18" rx="2" />
      <path d="M9 8h6M9 12h6M9 16h4" />
    </svg>
  );
}
