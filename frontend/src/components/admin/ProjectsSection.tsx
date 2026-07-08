'use client';

// Projects section of the Admin Console (ADR 12): full CRUD + reorder for
// projects, including unfeatured ones. Writes go to /api/admin/projects behind
// the server-enforced admin middleware; the admin gate on this UI is UX only.
//
// Images use presigned direct-to-S3 upload so the file bytes bypass the
// t4g.micro: we ask the backend for a presigned PUT, then PUT the raw File
// straight to S3. NOTE: the browser -> S3 PUT will fail with a CORS error until
// the S3 bucket CORS Terraform is applied (a separate gated manual step). The
// upload UI degrades gracefully (clear message, form still usable) until then;
// every other field works without uploads.

import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';

import {
  useListAdminProjects,
  useCreateProject,
  useUpdateProject,
  useDeleteProject,
  useReorderProjects,
  useCreateUploadPresign,
  getListAdminProjectsQueryKey,
} from '@/lib/api/generated';
import type { AdminProjectDTO } from '@/lib/api/model';
import { PresignUploadInputBodyContentType } from '@/lib/api/model';
import {
  citronCard,
  citronBadge,
  inputClass,
  labelClass,
  primaryBtn,
  ghostBtn,
  rowClass,
  editBtn,
  dangerBtn,
  iconBtn,
} from './ui';

interface ProjectForm {
  title: string;
  slug: string;
  summary: string;
  description: string;
  tagsText: string;
  repo_url: string;
  live_url: string;
  featured: boolean;
  sort_order: string;
  image_keys: string[];
}

const emptyForm: ProjectForm = {
  title: '',
  slug: '',
  summary: '',
  description: '',
  tagsText: '',
  repo_url: '',
  live_url: '',
  featured: false,
  sort_order: '0',
  image_keys: [],
};

function formToForm(p: AdminProjectDTO): ProjectForm {
  return {
    title: p.title,
    slug: p.slug,
    summary: p.summary,
    description: p.description,
    tagsText: (p.tags ?? []).join(', '),
    repo_url: p.repo_url,
    live_url: p.live_url,
    featured: p.featured,
    sort_order: String(p.sort_order),
    image_keys: p.image_keys ?? [],
  };
}

const allowedTypes = Object.values(PresignUploadInputBodyContentType) as string[];

export function ProjectsSection() {
  const queryClient = useQueryClient();
  const { data, isLoading } = useListAdminProjects();

  const projects = [...(data?.projects ?? [])].sort(
    (a, b) => a.sort_order - b.sort_order || a.id - b.id,
  );

  // null = closed, 'new' = create form, number = editing that project id.
  const [editing, setEditing] = useState<number | 'new' | null>(null);
  const [form, setForm] = useState<ProjectForm>(emptyForm);
  const [uploadError, setUploadError] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: getListAdminProjectsQueryKey() });

  const create = useCreateProject();
  const update = useUpdateProject();
  const remove = useDeleteProject();
  const reorder = useReorderProjects();
  const presign = useCreateUploadPresign();

  const saving = create.isPending || update.isPending;
  const listBusy = reorder.isPending || remove.isPending;

  const openNew = () => {
    setForm(emptyForm);
    setUploadError(null);
    setEditing('new');
  };
  const openEdit = (p: AdminProjectDTO) => {
    setForm(formToForm(p));
    setUploadError(null);
    setEditing(p.id);
  };
  const close = () => {
    setEditing(null);
    setUploadError(null);
  };

  const patch = <K extends keyof ProjectForm>(key: K, value: ProjectForm[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  const buildPayload = () => ({
    title: form.title.trim(),
    slug: form.slug.trim(),
    summary: form.summary.trim(),
    description: form.description,
    tags: form.tagsText
      .split(',')
      .map((t) => t.trim())
      .filter(Boolean),
    repo_url: form.repo_url.trim(),
    live_url: form.live_url.trim(),
    featured: form.featured,
    sort_order: Number.parseInt(form.sort_order, 10) || 0,
    image_keys: form.image_keys,
  });

  const canSave =
    form.title.trim().length > 0 &&
    form.slug.trim().length > 0 &&
    form.summary.trim().length > 0 &&
    !saving;

  const save = () => {
    if (!canSave) return;
    const payload = buildPayload();
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

  const del = (p: AdminProjectDTO) => {
    if (!confirm(`Delete "${p.title}"? This cannot be undone.`)) return;
    remove.mutate({ id: p.id }, { onSuccess: invalidate });
  };

  const move = (index: number, dir: -1 | 1) => {
    const j = index + dir;
    if (j < 0 || j >= projects.length) return;
    const next = [...projects];
    [next[index], next[j]] = [next[j], next[index]];
    reorder.mutate(
      { data: { items: next.map((p, i) => ({ id: p.id, sort_order: i })) } },
      { onSuccess: invalidate },
    );
  };

  // Presigned upload: ask the backend for a PUT URL, then PUT the raw File to S3.
  // Blocked by CORS until the bucket CORS Terraform lands — handled gracefully.
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
      { data: { content_type } },
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
              patch('image_keys', [...form.image_keys, out.key]);
            } catch {
              // A CORS rejection surfaces as a TypeError ("Failed to fetch").
              setUploadError(
                'Upload failed. Browser-to-S3 uploads are blocked until the ' +
                  'bucket CORS rule is applied (a separate deploy step). The ' +
                  'rest of the form still saves.',
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

  const removeImage = (key: string) =>
    patch(
      'image_keys',
      form.image_keys.filter((k) => k !== key),
    );

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
            <ProjectsGlyph />
          </span>
          <div>
            <h2 className="font-display text-lg font-bold text-white">
              Projects
            </h2>
            <p className="text-sm text-slate-400">
              Create, edit, reorder and remove portfolio projects.
            </p>
          </div>
        </div>
        {editing === null && (
          <button
            type="button"
            onClick={openNew}
            className="shrink-0 rounded-lg border border-slate-700 px-3 py-1.5 text-sm font-medium text-white transition hover:border-citron hover:text-citron"
          >
            New project
          </button>
        )}
      </header>

      {editing !== null && (
        <div className="flex flex-col gap-4 rounded-xl border border-slate-700 bg-deepsea/40 p-4">
          <div className="grid gap-3 sm:grid-cols-2">
            <label className={labelClass}>
              Title
              <input
                className={inputClass}
                value={form.title}
                onChange={(e) => patch('title', e.target.value)}
              />
            </label>
            <label className={labelClass}>
              Slug
              <input
                className={inputClass}
                placeholder="url-friendly-id"
                value={form.slug}
                onChange={(e) => patch('slug', e.target.value)}
              />
            </label>
          </div>

          <label className={labelClass}>
            Summary
            <input
              className={inputClass}
              maxLength={280}
              placeholder="Short one-liner for cards"
              value={form.summary}
              onChange={(e) => patch('summary', e.target.value)}
            />
          </label>

          <label className={labelClass}>
            Description (markdown)
            <textarea
              className={inputClass}
              rows={5}
              value={form.description}
              onChange={(e) => patch('description', e.target.value)}
            />
          </label>

          <label className={labelClass}>
            Tags (comma-separated)
            <input
              className={inputClass}
              placeholder="go, next.js, aws"
              value={form.tagsText}
              onChange={(e) => patch('tagsText', e.target.value)}
            />
          </label>

          <div className="grid gap-3 sm:grid-cols-2">
            <label className={labelClass}>
              Repo URL
              <input
                className={inputClass}
                inputMode="url"
                placeholder="https://github.com/…"
                value={form.repo_url}
                onChange={(e) => patch('repo_url', e.target.value)}
              />
            </label>
            <label className={labelClass}>
              Live URL
              <input
                className={inputClass}
                inputMode="url"
                placeholder="https://…"
                value={form.live_url}
                onChange={(e) => patch('live_url', e.target.value)}
              />
            </label>
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            <label className="flex items-center gap-2 text-sm text-slate-300">
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--color-citron)]"
                checked={form.featured}
                onChange={(e) => patch('featured', e.target.checked)}
              />
              Featured
            </label>
            <label className={labelClass}>
              Sort order (lower first)
              <input
                className={inputClass}
                type="number"
                value={form.sort_order}
                onChange={(e) => patch('sort_order', e.target.value)}
              />
            </label>
          </div>

          {/* Images: presigned direct-to-S3 upload (CORS-gated, see top). */}
          <div className="flex flex-col gap-2">
            <span className="text-sm text-slate-300">Images</span>
            {form.image_keys.length > 0 ? (
              <ul className="flex flex-wrap gap-2">
                {form.image_keys.map((key) => (
                  <li
                    key={key}
                    className="flex items-center gap-2 rounded-md border border-slate-700 bg-deepsea px-2.5 py-1 text-xs text-slate-300"
                  >
                    <span className="max-w-[16rem] truncate font-mono">
                      {key}
                    </span>
                    <button
                      type="button"
                      aria-label={`Remove image ${key}`}
                      onClick={() => removeImage(key)}
                      className="text-coral transition hover:brightness-110"
                    >
                      ✕
                    </button>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="text-xs text-slate-400">No images attached.</p>
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

      {/* Project list */}
      {isLoading ? (
        <p className="text-sm text-slate-400">Loading…</p>
      ) : projects.length === 0 ? (
        <p className="text-sm text-slate-400">No projects yet.</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {projects.map((p, i) => (
            <li key={p.id} className={rowClass}>
              <div className="min-w-0">
                <p className="flex items-center gap-2 font-medium text-white">
                  <span className="truncate">{p.title}</span>
                  {p.featured ? (
                    <span className="shrink-0 rounded-full bg-citron/20 px-2 py-0.5 font-mono text-[0.6rem] uppercase tracking-widest text-citron">
                      featured
                    </span>
                  ) : null}
                </p>
                <p className="mt-0.5 truncate text-sm text-slate-400">
                  <span className="font-mono">{p.slug}</span> · {p.summary}
                </p>
              </div>
              <div className="flex shrink-0 items-center gap-1.5">
                <button
                  type="button"
                  aria-label="Move up"
                  disabled={i === 0 || listBusy}
                  onClick={() => move(i, -1)}
                  className={iconBtn}
                >
                  ↑
                </button>
                <button
                  type="button"
                  aria-label="Move down"
                  disabled={i === projects.length - 1 || listBusy}
                  onClick={() => move(i, 1)}
                  className={iconBtn}
                >
                  ↓
                </button>
                <button
                  type="button"
                  onClick={() => openEdit(p)}
                  className={`${editBtn} ml-1`}
                >
                  Edit
                </button>
                <button
                  type="button"
                  onClick={() => del(p)}
                  disabled={listBusy}
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

function ProjectsGlyph() {
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
      <path d="M4 4h6l2 2h8v12a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2z" />
    </svg>
  );
}
