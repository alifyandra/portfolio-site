// Shared visual tokens for the Admin Console sections, so Projects / Friends /
// Playlists read as one family. Citron is the admin accent (matches the navbar
// account chip + /account page). Mirrors the whatsapp panels' idiom: literal
// utilities for inputs/buttons, CSS-var color-mix for the tinted card surfaces
// (safe in both themes, since the vars flip per theme).
import type { CSSProperties } from 'react';

export const inputClass =
  'w-full rounded-lg border border-slate-700 bg-deepsea px-3 py-2 text-white outline-none transition focus:border-sky';

export const labelClass = 'flex flex-col gap-1 text-sm text-slate-300';

export const primaryBtn =
  'rounded-lg bg-citron px-4 py-2 text-sm font-semibold text-ink transition hover:brightness-95 disabled:cursor-not-allowed disabled:opacity-50';

export const ghostBtn =
  'rounded-lg border border-slate-700 px-4 py-2 text-sm text-white transition hover:border-slate-500 disabled:cursor-not-allowed disabled:opacity-50';

// Citron-tinted card surface (the admin family).
export const citronCard: CSSProperties = {
  borderColor: 'color-mix(in srgb, var(--color-citron) 32%, transparent)',
  background: 'color-mix(in srgb, var(--color-citron) 6%, var(--color-deepsea))',
};

// Small icon badge inside a section header.
export const citronBadge: CSSProperties = {
  background: 'color-mix(in srgb, var(--color-citron) 16%, transparent)',
};

// A single list row (project / grant / playlist).
export const rowClass =
  'flex items-start justify-between gap-4 rounded-lg border border-slate-700 bg-deepsea/40 p-3';
