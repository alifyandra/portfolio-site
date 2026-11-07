'use client';

// Recurrence suggestions (ADR 0019): runs of posted transactions the detection pass
// found (same counterparty, near-constant amount, near-constant interval), offered for
// review. The pass only suggests. Accept creates the Recurring Bill or Recurring Income
// and links its history; Dismiss is remembered, so the run is not offered again. Every
// route is under /api/admin/finance/suggestions behind the server-enforced admin gate.
// The pure text rules live in suggestionRules.ts, where node --test reaches them.

import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';

import {
  useListFinanceSuggestions,
  useScanFinanceSuggestions,
  useAcceptFinanceSuggestion,
  useDismissFinanceSuggestion,
  getListFinanceSuggestionsQueryKey,
  getListFinanceBillsQueryKey,
  getListFinanceIncomeQueryKey,
} from '@/lib/api/generated';
import type {
  FinanceSuggestionDTO,
  ScanFinanceSuggestionsOutputBody,
} from '@/lib/api/model';
import {
  citronCard,
  citronBadge,
  primaryBtn,
  editBtn,
  dangerBtn,
} from '@/components/admin/ui';
import { formatDate } from './format';
import { useMoney } from './censor';
import {
  scanSummaryText,
  kindLabel,
  evidenceSpanText,
  acceptPromptText,
  dismissPromptText,
} from './suggestionRules';

const CADENCE_LABELS: Record<string, string> = {
  weekly: 'Weekly',
  fortnightly: 'Fortnightly',
  monthly: 'Monthly',
  quarterly: 'Quarterly',
  annual: 'Annual',
};

const rowGhostBtn =
  'rounded-md border border-slate-700 px-2.5 py-1 text-xs font-semibold text-white transition hover:border-slate-500 disabled:cursor-not-allowed disabled:opacity-50';

export function SuggestionsSection() {
  const queryClient = useQueryClient();
  const { money } = useMoney();
  const { data, isLoading, isError } = useListFinanceSuggestions({
    status: 'pending',
  });
  const suggestions = data?.suggestions ?? [];

  const [lastScan, setLastScan] =
    useState<ScanFinanceSuggestionsOutputBody | null>(null);
  const [openEvidence, setOpenEvidence] = useState<number | null>(null);

  const scan = useScanFinanceSuggestions();
  const accept = useAcceptFinanceSuggestion();
  const dismiss = useDismissFinanceSuggestion();
  const busy = scan.isPending || accept.isPending || dismiss.isPending;
  const error = scan.error ?? accept.error ?? dismiss.error;

  const invalidateSuggestions = () =>
    queryClient.invalidateQueries({
      queryKey: getListFinanceSuggestionsQueryKey(),
    });

  const runScan = () =>
    scan.mutate(undefined, {
      onSuccess: (sum) => {
        setLastScan(sum);
        invalidateSuggestions();
      },
    });

  const onAccept = (s: FinanceSuggestionDTO) => {
    const name = prompt(acceptPromptText(s.kind), s.label)?.trim();
    if (!name) return;
    accept.mutate(
      { id: s.id, data: { name } },
      {
        onSuccess: () => {
          invalidateSuggestions();
          // The new declaration shows up in its own section straight away.
          queryClient.invalidateQueries({
            queryKey: getListFinanceBillsQueryKey(),
          });
          queryClient.invalidateQueries({
            queryKey: getListFinanceIncomeQueryKey(),
          });
        },
      },
    );
  };

  const onDismiss = (s: FinanceSuggestionDTO) => {
    if (!confirm(dismissPromptText(s.label))) return;
    dismiss.mutate({ id: s.id }, { onSuccess: invalidateSuggestions });
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
            <RepeatGlyph />
          </span>
          <div>
            <h2 className="font-display text-lg font-bold text-white">
              Suggested recurring
            </h2>
            <p className="text-sm text-slate-400">
              Regular payments found in the ledger, to accept or dismiss.
            </p>
          </div>
        </div>
        <button
          type="button"
          className={primaryBtn}
          disabled={busy}
          onClick={runScan}
        >
          {scan.isPending ? 'Scanning…' : 'Scan history'}
        </button>
      </header>

      {lastScan ? (
        <p className="text-sm text-slate-300">{scanSummaryText(lastScan)}</p>
      ) : null}
      {error ? (
        <p className="text-sm text-coral">{(error as Error).message}</p>
      ) : null}

      {isError ? (
        <p className="text-sm text-coral">Could not load suggestions.</p>
      ) : isLoading ? (
        <p className="text-sm text-slate-400">Loading suggestions…</p>
      ) : suggestions.length === 0 ? (
        <p className="text-sm text-slate-400">
          Nothing to review. Scan history to look for regular payments.
        </p>
      ) : (
        <ul className="flex flex-col gap-2">
          {suggestions.map((s) => (
            <li
              key={s.id}
              className="flex flex-col gap-2 rounded-lg border border-slate-700 bg-deepsea/40 p-3"
            >
              <div className="flex items-start justify-between gap-4">
                <div className="flex min-w-0 flex-col gap-0.5">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate font-medium text-white">
                      {s.label}
                    </span>
                    <span className="rounded-sm bg-white/5 px-1.5 py-0.5 text-xs text-slate-300">
                      {kindLabel(s.kind)}
                    </span>
                    <span className="font-mono text-xs uppercase tracking-wide text-slate-400">
                      {CADENCE_LABELS[s.cadence] ?? s.cadence}
                    </span>
                  </div>
                  <span className="truncate text-xs text-slate-400">
                    {evidenceSpanText(s.kind, s.evidence ?? [], formatDate)}
                    {s.account_name ? ` · ${s.account_name}` : ''} · next{' '}
                    {formatDate(s.next_expected)}
                  </span>
                  <span className="truncate text-xs text-slate-400">
                    {s.match_pattern ? (
                      <>
                        Matches{' '}
                        <span className="font-mono text-slate-300">
                          {s.match_pattern}
                        </span>
                      </>
                    ) : (
                      'No shared text to match on; payments link by hand'
                    )}
                  </span>
                </div>
                <div className="flex shrink-0 flex-col items-end gap-1.5">
                  <span className="text-sm font-semibold tabular-nums text-white">
                    {money(s.expected_amount)}
                  </span>
                  <div className="flex items-center gap-1.5">
                    <button
                      type="button"
                      className={rowGhostBtn}
                      aria-expanded={openEvidence === s.id}
                      onClick={() =>
                        setOpenEvidence((cur) => (cur === s.id ? null : s.id))
                      }
                    >
                      {openEvidence === s.id ? 'Hide evidence' : 'Evidence'}
                    </button>
                    <button
                      type="button"
                      className={editBtn}
                      disabled={busy}
                      onClick={() => onAccept(s)}
                    >
                      Accept
                    </button>
                    <button
                      type="button"
                      className={dangerBtn}
                      disabled={busy}
                      onClick={() => onDismiss(s)}
                    >
                      Dismiss
                    </button>
                  </div>
                </div>
              </div>
              {openEvidence === s.id ? (
                <ul className="flex flex-col divide-y divide-slate-700/60 border-t border-slate-700 pt-1">
                  {(s.evidence ?? []).map((e) => (
                    <li
                      key={e.transaction_id}
                      className="flex items-center justify-between gap-3 py-1.5 text-xs"
                    >
                      <span className="min-w-0 truncate text-slate-400">
                        <span className="text-slate-200">
                          {formatDate(e.posted_date)}
                        </span>{' '}
                        · {e.description}
                      </span>
                      <span className="shrink-0 tabular-nums text-white">
                        {money(e.amount)}
                      </span>
                    </li>
                  ))}
                </ul>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function RepeatGlyph() {
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
      <path d="M17 2l4 4-4 4" />
      <path d="M3 11v-1a4 4 0 0 1 4-4h14" />
      <path d="M7 22l-4-4 4-4" />
      <path d="M21 13v1a4 4 0 0 1-4 4H3" />
    </svg>
  );
}
