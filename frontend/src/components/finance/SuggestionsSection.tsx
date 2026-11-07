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
  useGetFinanceSuggestion,
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
  ghostBtn,
  editBtn,
  dangerBtn,
  inputClass,
  labelClass,
} from '@/components/admin/ui';
import { formatDate } from './format';
import { useMoney } from './censor';
import {
  scanSummaryText,
  kindLabel,
  evidenceSpanText,
  evidenceMoreText,
  acceptIntroText,
  acceptPatternHint,
  acceptButtonText,
  dismissPromptText,
} from './suggestionRules';

// The declaration's own byte caps (Ent MaxLen counts bytes; the API pre-checks them).
const NAME_MAX_BYTES = 120;
const PATTERN_MAX_BYTES = 200;
const byteLength = (s: string) => new TextEncoder().encode(s).length;

/** The open accept form: which suggestion, and the name and pattern the owner is typing. */
interface AcceptDraft {
  id: number;
  name: string;
  pattern: string;
}

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
  const [draft, setDraft] = useState<AcceptDraft | null>(null);

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

  // Accepting opens a form rather than acting at once: the match pattern is the owner's
  // (ADR 0019), so the suggested one is shown, editable, and only used if they confirm it.
  const openAccept = (s: FinanceSuggestionDTO) =>
    setDraft({ id: s.id, name: s.label, pattern: s.match_pattern });

  const onAccept = (s: FinanceSuggestionDTO, d: AcceptDraft) => {
    const name = d.name.trim();
    if (!name) return;
    accept.mutate(
      { id: s.id, data: { name, match_pattern: d.pattern.trim() } },
      {
        onSuccess: () => {
          setDraft(null);
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
                    {evidenceSpanText(
                      s.kind,
                      {
                        count: s.evidence_count,
                        since: s.evidence_since,
                        newest:
                          s.evidence?.[0]?.posted_date ?? s.evidence_since,
                        gaps: s.gaps,
                      },
                      formatDate,
                    )}
                    {s.account_name ? ` · ${s.account_name}` : ''} · next{' '}
                    {formatDate(s.next_expected)}
                  </span>
                  <span className="truncate text-xs text-slate-400">
                    {s.match_pattern ? (
                      <>
                        Suggested pattern{' '}
                        <span className="font-mono text-slate-300">
                          {s.match_pattern}
                        </span>
                      </>
                    ) : (
                      'No shared text to suggest a pattern from'
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
                      disabled={busy || draft?.id === s.id}
                      onClick={() => openAccept(s)}
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
              {draft?.id === s.id ? (
                <AcceptForm
                  suggestion={s}
                  draft={draft}
                  busy={busy}
                  onChange={setDraft}
                  onCancel={() => setDraft(null)}
                  onSubmit={() => onAccept(s, draft)}
                />
              ) : null}
              {openEvidence === s.id ? <EvidenceList suggestion={s} /> : null}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/**
 * One suggestion's evidence. The list response carries only the newest few rows, so
 * "Show all" fetches the suggestion on its own for the rest.
 */
function EvidenceList({ suggestion: s }: { suggestion: FinanceSuggestionDTO }) {
  const { money } = useMoney();
  const [all, setAll] = useState(false);
  const detail = useGetFinanceSuggestion(s.id, { query: { enabled: all } });
  const rows = (all ? detail.data?.evidence : undefined) ?? s.evidence ?? [];
  const more = evidenceMoreText(rows.length, s.evidence_count);
  return (
    <div className="flex flex-col gap-1 border-t border-slate-700 pt-1">
      <ul className="flex flex-col divide-y divide-slate-700/60">
        {rows.map((e) => (
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
      {detail.isError ? (
        <p className="text-xs text-coral">Could not load the rest.</p>
      ) : more ? (
        <button
          type="button"
          className="self-start text-xs text-sky underline-offset-2 hover:underline disabled:opacity-50"
          disabled={all && detail.isFetching}
          onClick={() => setAll(true)}
        >
          {all && detail.isFetching ? 'Loading…' : more}
        </button>
      ) : null}
    </div>
  );
}

/**
 * The accept step: the declaration's name and its match pattern, both editable. The
 * pattern starts as the suggested one and says so; the owner confirms it by creating,
 * edits it, or clears it to link by hand.
 */
function AcceptForm({
  suggestion: s,
  draft,
  busy,
  onChange,
  onCancel,
  onSubmit,
}: {
  suggestion: FinanceSuggestionDTO;
  draft: AcceptDraft;
  busy: boolean;
  onChange: (d: AcceptDraft) => void;
  onCancel: () => void;
  onSubmit: () => void;
}) {
  const nameBytes = byteLength(draft.name.trim());
  const patternBytes = byteLength(draft.pattern.trim());
  const invalid =
    draft.name.trim() === '' ||
    nameBytes > NAME_MAX_BYTES ||
    patternBytes > PATTERN_MAX_BYTES;
  return (
    <form
      className="flex flex-col gap-3 border-t border-slate-700 pt-3"
      onSubmit={(e) => {
        e.preventDefault();
        if (!invalid) onSubmit();
      }}
    >
      <p className="text-xs text-slate-400">{acceptIntroText(s.kind)}</p>
      <div className="grid gap-3 sm:grid-cols-2">
        <label className={labelClass}>
          Name
          <input
            type="text"
            className={inputClass}
            value={draft.name}
            onChange={(e) => onChange({ ...draft, name: e.target.value })}
          />
        </label>
        <label className={labelClass}>
          Match pattern
          <input
            type="text"
            className={`${inputClass} font-mono`}
            value={draft.pattern}
            onChange={(e) => onChange({ ...draft, pattern: e.target.value })}
          />
        </label>
      </div>
      <p className="text-xs text-slate-400">
        {acceptPatternHint(s.kind, s.match_pattern, draft.pattern)}
      </p>
      {nameBytes > NAME_MAX_BYTES ? (
        <p className="text-xs text-coral">
          The name is {nameBytes} bytes; the limit is {NAME_MAX_BYTES}.
        </p>
      ) : null}
      {patternBytes > PATTERN_MAX_BYTES ? (
        <p className="text-xs text-coral">
          The pattern is {patternBytes} bytes; the limit is {PATTERN_MAX_BYTES}.
        </p>
      ) : null}
      <div className="flex flex-wrap items-center gap-2">
        <button type="submit" className={primaryBtn} disabled={busy || invalid}>
          {acceptButtonText(s.kind, draft.pattern)}
        </button>
        <button type="button" className={ghostBtn} onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
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
