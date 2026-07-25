// Shared formatting helpers for the private finance dashboard (/finance).
// All money is AUD (the finance domain is a single-currency CommBank export;
// see CONTEXT.md), so we format with en-AU / AUD and lean on the DTO's
// `currency` field only for display, never to switch locale.

import type { FinanceAccountDTOType } from '@/lib/api/model';

const audFmt = new Intl.NumberFormat('en-AU', {
  style: 'currency',
  currency: 'AUD',
});

/** "$1,234.56" — the sign is preserved (negative renders as "-$12.00"). */
export function formatMoney(value: number): string {
  return audFmt.format(value);
}

/** Magnitude only, e.g. for a liability shown as an amount "owed". */
export function formatAbs(value: number): string {
  return audFmt.format(Math.abs(value));
}

/** YYYY-MM-DD (or RFC3339) -> "24 Jul 2026". Returns "" for null/empty. */
export function formatDate(value: string | null | undefined): string {
  if (!value) return '';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return d.toLocaleDateString('en-AU', {
    day: 'numeric',
    month: 'short',
    year: 'numeric',
  });
}

/** RFC3339 -> "24 Jul 2026, 6:21 pm". Returns "" for null/empty. */
export function formatDateTime(value: string | null | undefined): string {
  if (!value) return '';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return d.toLocaleString('en-AU', {
    day: 'numeric',
    month: 'short',
    year: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  });
}

const TYPE_LABELS: Record<FinanceAccountDTOType, string> = {
  everyday: 'Everyday',
  savings: 'Savings',
  credit_card: 'Credit Card',
  steppay: 'StepPay',
  investment: 'Investment',
};

/** Human label for an account type; falls back to the raw value defensively. */
export function accountTypeLabel(type: FinanceAccountDTOType | string): string {
  return TYPE_LABELS[type as FinanceAccountDTOType] ?? String(type);
}
