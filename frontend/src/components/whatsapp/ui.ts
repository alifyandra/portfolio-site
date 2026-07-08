// Shared visual tokens for the WhatsApp tool panels, so Templates / Lists /
// Send / CountryCode read as one family (mirrors admin/ui.ts). Row actions are
// filled accent buttons (Edit = sky, Delete / danger = coral) rather than bare
// coloured text; selects get the house dropdown caret (see .select-caret in
// globals.css) plus right padding so the glyph never crowds the border.

export const selectClass =
  'w-full rounded-lg border border-slate-700 bg-deepsea py-2 pl-3 pr-10 text-white outline-none transition focus:border-sky select-caret';

export const editBtn =
  'rounded-md bg-sky px-2.5 py-1 text-xs font-semibold text-ink transition hover:brightness-95 disabled:cursor-not-allowed disabled:opacity-50';

export const dangerBtn =
  'rounded-md bg-coral px-2.5 py-1 text-xs font-semibold text-ink transition hover:brightness-95 disabled:cursor-not-allowed disabled:opacity-50';
