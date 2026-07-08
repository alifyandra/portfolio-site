'use client';

import { useEffect, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { QRCodeSVG } from 'qrcode.react';

import {
  useListWaTemplates,
  useListWaLists,
  useListWaBatches,
  getWaList,
  getListWaBatchesQueryKey,
} from '@/lib/api/generated';
import type { BatchDTO } from '@/lib/api/model';
import { streamBatch, type WaEvent } from '@/lib/wa-stream';
import { selectClass } from './ui';

type Phase = 'idle' | 'starting' | 'provisioning' | 'linking' | 'running' | 'done' | 'error';
type RowStatus = 'pending' | 'sent' | 'skipped' | 'failed';

interface Row {
  phone: string;
  name?: string;
  status: RowStatus;
  reason?: string;
  error?: string;
}

// mint-accented card (the "send" concept — green like WhatsApp / go).
const cardStyle = {
  borderColor: 'color-mix(in srgb, var(--color-mint) 40%, transparent)',
  background: 'color-mix(in srgb, var(--color-mint) 7%, var(--color-deepsea))',
};
const badgeStyle = {
  background: 'color-mix(in srgb, var(--color-mint) 16%, transparent)',
};

// Accent (a CSS var) per batch status, used to tint the Recent-batches cards
// and their status pill. Falls back to slate for anything unknown.
const statusAccent: Record<string, string> = {
  completed: 'var(--color-mint)',
  failed: 'var(--color-coral)',
  running: 'var(--color-sky)',
  linking: 'var(--color-sky)',
  pending: 'var(--color-slate-500)',
};

const rowStatusStyle: Record<RowStatus, string> = {
  pending: 'text-slate-500',
  sent: 'text-mint',
  skipped: 'text-slate-400',
  failed: 'text-coral',
};

function updateRow(prev: Row[], ev: Extract<WaEvent, { type: 'progress' }>): Row[] {
  const idx = prev.findIndex((r) => r.phone === ev.phone && r.status === 'pending');
  if (idx === -1) {
    // Prefetch missed this number (or it was empty): append it.
    return [...prev, { phone: ev.phone, name: ev.name, status: ev.status, reason: ev.reason, error: ev.error }];
  }
  const copy = prev.slice();
  copy[idx] = {
    ...copy[idx],
    name: copy[idx].name || ev.name,
    status: ev.status,
    reason: ev.reason,
    error: ev.error,
  };
  return copy;
}

export function SendPanel() {
  const queryClient = useQueryClient();
  const { data: templatesData } = useListWaTemplates();
  const { data: listsData } = useListWaLists();
  const { data: batchesData } = useListWaBatches();

  const templates = templatesData?.templates ?? [];
  const lists = listsData?.lists ?? [];
  const batches = batchesData?.batches ?? [];
  const dailyRemaining = batchesData?.daily_remaining ?? 0;

  const [templateId, setTemplateId] = useState<number | ''>('');
  const [listId, setListId] = useState<number | ''>('');

  const [phase, setPhase] = useState<Phase>('idle');
  const [qr, setQr] = useState<string | null>(null);
  const [rows, setRows] = useState<Row[]>([]);
  const [message, setMessage] = useState<string | null>(null);
  const [countdown, setCountdown] = useState<number | null>(null);
  const [nextPhone, setNextPhone] = useState<string | null>(null);

  const abortRef = useRef<AbortController | null>(null);
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const stopCountdown = () => {
    if (timerRef.current) {
      clearInterval(timerRef.current);
      timerRef.current = null;
    }
    setCountdown(null);
  };

  const startCountdown = (ms: number) => {
    stopCountdown();
    let remaining = Math.max(1, Math.ceil(ms / 1000));
    setCountdown(remaining);
    timerRef.current = setInterval(() => {
      remaining -= 1;
      if (remaining <= 0) stopCountdown();
      else setCountdown(remaining);
    }, 1000);
  };

  // Clear the interval if the component unmounts mid-send.
  useEffect(() => () => stopCountdown(), []);

  const selectedList = lists.find((l) => l.id === listId);
  const counts = {
    sent: rows.filter((r) => r.status === 'sent').length,
    skipped: rows.filter((r) => r.status === 'skipped').length,
    failed: rows.filter((r) => r.status === 'failed').length,
  };
  const total = rows.length || selectedList?.recipient_count || 0;
  const processed = counts.sent + counts.skipped + counts.failed;
  const active =
    phase === 'starting' || phase === 'provisioning' || phase === 'linking' || phase === 'running';
  const canSend =
    !active && templateId !== '' && listId !== '' && (selectedList?.recipient_count ?? 0) > 0 && dailyRemaining > 0;

  const nextRow = nextPhone ? rows.find((r) => r.phone === nextPhone) : null;
  const nextLabel = nextRow ? nextRow.name || nextRow.phone : nextPhone;

  const invalidateBatches = () =>
    queryClient.invalidateQueries({ queryKey: getListWaBatchesQueryKey() });

  const reset = () => {
    stopCountdown();
    setPhase('idle');
    setQr(null);
    setRows([]);
    setMessage(null);
    setNextPhone(null);
  };

  const onEvent = (ev: WaEvent, recips: Row[]) => {
    switch (ev.type) {
      case 'provisioning':
        // Fargate cold start: keep the user informed while the sidecar boots.
        // Only ever arrives before the first `qr`; carries a short human status.
        setPhase('provisioning');
        if (ev.message) setMessage(ev.message);
        break;
      case 'qr':
        setQr(ev.value);
        setPhase('linking');
        break;
      case 'ready':
        setPhase('running');
        setNextPhone(recips[0]?.phone ?? null); // the first send starts immediately
        break;
      case 'progress':
        stopCountdown();
        setNextPhone(null);
        setRows((prev) => updateRow(prev, ev));
        break;
      case 'waiting':
        setNextPhone(ev.next_phone ?? null);
        startCountdown(ev.ms);
        break;
      case 'done':
        stopCountdown();
        setNextPhone(null);
        setMessage(`Done. Sent ${ev.sent}, skipped ${ev.skipped}, failed ${ev.failed}.`);
        setPhase('done');
        break;
      case 'error':
        stopCountdown();
        setNextPhone(null);
        setMessage(ev.message);
        setPhase('error');
        break;
    }
  };

  const send = async () => {
    if (templateId === '' || listId === '') return;
    reset();
    setPhase('starting');

    // Prefetch the recipients so the whole list is visible up front, each row
    // flipping from queued to its outcome as events arrive.
    let recips: Row[] = [];
    try {
      const full = await getWaList(listId);
      recips = (full.list.recipients ?? []).map((r) => ({
        phone: r.phone,
        name: r.name,
        status: 'pending' as RowStatus,
      }));
    } catch {
      recips = []; // fall back to appending rows from progress events
    }
    setRows(recips);

    const controller = new AbortController();
    abortRef.current = controller;
    try {
      await streamBatch({ template_id: templateId, list_id: listId }, (ev) => onEvent(ev, recips), controller.signal);
    } catch (err) {
      if (controller.signal.aborted) {
        setPhase('idle');
      } else {
        setMessage(err instanceof Error ? err.message : 'The send failed.');
        setPhase('error');
      }
    } finally {
      stopCountdown();
      setNextPhone(null);
      abortRef.current = null;
      invalidateBatches();
    }
  };

  const cancel = () => abortRef.current?.abort();

  return (
    <section
      className="flex flex-col gap-4 rounded-2xl border p-5 sm:p-6"
      style={cardStyle}
    >
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <span
            className="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-xl text-mint"
            style={badgeStyle}
          >
            <SendGlyph />
          </span>
          <h2 className="font-display text-lg font-bold text-white">Send</h2>
        </div>
        <span className="font-mono text-xs uppercase tracking-widest text-slate-400">
          {dailyRemaining} send{dailyRemaining === 1 ? '' : 's'} left today
        </span>
      </div>

      {/* Configure + trigger */}
      {!active && phase !== 'done' && (
        <div className="flex flex-col gap-3">
          <label className="flex flex-col gap-1 text-sm text-slate-300">
            Template
            <select
              className={selectClass}
              value={templateId}
              onChange={(e) => setTemplateId(e.target.value ? Number(e.target.value) : '')}
            >
              <option value="">Choose a template…</option>
              {templates.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </select>
          </label>

          <label className="flex flex-col gap-1 text-sm text-slate-300">
            Recipient list
            <select
              className={selectClass}
              value={listId}
              onChange={(e) => setListId(e.target.value ? Number(e.target.value) : '')}
            >
              <option value="">Choose a list…</option>
              {lists.map((l) => (
                <option key={l.id} value={l.id}>
                  {l.name} ({l.recipient_count})
                </option>
              ))}
            </select>
          </label>

          {dailyRemaining === 0 && (
            <p className="text-sm text-coral">
              You have reached the daily send limit. Try again tomorrow.
            </p>
          )}
          {phase === 'error' && message && <p className="text-sm text-coral">{message}</p>}

          <button
            type="button"
            disabled={!canSend}
            onClick={send}
            className="self-start rounded-lg bg-citron px-5 py-2 font-semibold text-ink transition hover:brightness-95 disabled:opacity-50"
          >
            Send batch
          </button>
          <p className="text-xs text-slate-400">
            After you click send, scan the QR code with WhatsApp on your phone
            (Settings → Linked Devices). Sending starts the moment it links.
          </p>
        </div>
      )}

      {/* Linking: show the QR */}
      {phase === 'linking' && qr && (
        <div className="flex flex-col items-center gap-3">
          <p className="text-sm text-slate-300">
            Scan with WhatsApp → Settings → Linked Devices
          </p>
          {/* literal white required so the QR stays scannable in both themes */}
          <div className="inline-block rounded-xl bg-[#ffffff] p-3">
            <QRCodeSVG value={qr} size={220} />
          </div>
          <button
            type="button"
            onClick={cancel}
            className="rounded-lg border border-slate-700 px-4 py-1.5 text-sm text-white transition hover:border-coral hover:text-coral"
          >
            Cancel
          </button>
        </div>
      )}

      {phase === 'starting' && (
        <div className="flex items-center gap-2 text-sm text-slate-300">
          <span className="h-2 w-2 animate-pulse rounded-full bg-sky" />
          Starting a session…
        </div>
      )}

      {/* Provisioning: the sender is cold-starting (Fargate), no QR yet */}
      {phase === 'provisioning' && (
        <div className="flex flex-col gap-3">
          <div className="flex items-center gap-2 text-sm text-slate-300">
            <span className="h-2 w-2 animate-pulse rounded-full bg-sky" />
            Starting the WhatsApp sender…
          </div>
          <p className="text-xs text-slate-400">
            This can take up to a minute the first time. The QR code will appear
            as soon as the sender is ready.
          </p>
          {message && <p className="text-xs text-sky">{message}</p>}
          <button
            type="button"
            onClick={cancel}
            className="self-start rounded-lg border border-slate-700 px-4 py-1.5 text-sm text-white transition hover:border-coral hover:text-coral"
          >
            Cancel
          </button>
        </div>
      )}

      {/* Running / done: full recipient list with live status + countdown */}
      {(phase === 'running' || phase === 'done') && (
        <div className="flex flex-col gap-3">
          <div className="flex items-center justify-between">
            <span className="text-sm text-slate-300">
              {phase === 'running' ? 'Sending…' : 'Complete'}
            </span>
            <span className="font-mono text-xs uppercase tracking-widest text-slate-400">
              {processed}/{total}
            </span>
          </div>
          <div className="flex gap-4 text-sm">
            <span className="text-mint">Sent {counts.sent}</span>
            <span className="text-slate-400">Skipped {counts.skipped}</span>
            <span className="text-coral">Failed {counts.failed}</span>
          </div>

          {/* Countdown / current action banner */}
          {phase === 'running' && countdown !== null && (
            <p className="rounded-lg border border-sky/40 bg-sky/10 px-3 py-2 text-sm text-sky">
              Next message in {countdown}s{nextLabel ? ` → ${nextLabel}` : ''}
            </p>
          )}
          {phase === 'running' && countdown === null && nextPhone && (
            <p className="rounded-lg border border-sky/40 bg-sky/10 px-3 py-2 text-sm text-sky">
              Sending to {nextLabel}…
            </p>
          )}

          {rows.length > 0 && (
            <ul className="max-h-72 overflow-y-auto rounded-lg border border-slate-800 bg-deepsea p-2 font-mono text-xs">
              {rows.map((r, i) => {
                const isNext = phase === 'running' && r.phone === nextPhone;
                return (
                  <li
                    key={`${r.phone}-${i}`}
                    className={`flex justify-between gap-2 rounded px-1 py-0.5 ${isNext ? 'bg-sky/10' : ''}`}
                  >
                    <span className="truncate text-slate-300">
                      {r.phone}
                      {r.name ? ` (${r.name})` : ''}
                    </span>
                    <span className={rowStatusStyle[r.status]}>
                      {isNext && countdown !== null
                        ? `next · ${countdown}s`
                        : isNext
                          ? 'sending…'
                          : r.status === 'pending'
                            ? 'queued'
                            : r.status === 'skipped'
                              ? `skipped${r.reason ? ` · ${r.reason}` : ''}`
                              : r.status}
                    </span>
                  </li>
                );
              })}
            </ul>
          )}

          {phase === 'running' && (
            <button
              type="button"
              onClick={cancel}
              className="self-start rounded-lg border border-slate-700 px-4 py-1.5 text-sm text-white transition hover:border-coral hover:text-coral"
            >
              Cancel
            </button>
          )}
          {phase === 'done' && (
            <button
              type="button"
              onClick={reset}
              className="self-start rounded-lg border border-slate-700 px-4 py-1.5 text-sm text-white transition hover:border-citron hover:text-citron"
            >
              Send another
            </button>
          )}
        </div>
      )}

      {/* History */}
      {batches.length > 0 && (
        <div className="flex flex-col gap-2 border-t border-slate-800 pt-4">
          <h3 className="font-mono text-xs uppercase tracking-widest text-slate-400">
            Recent batches
          </h3>
          <ul className="flex flex-col gap-2">
            {batches.map((b: BatchDTO) => {
              const accent = statusAccent[b.status] ?? 'var(--color-slate-500)';
              return (
                <li
                  key={b.id}
                  className="flex items-center justify-between gap-3 rounded-xl border p-3"
                  style={{
                    borderColor: `color-mix(in srgb, ${accent} 35%, transparent)`,
                    background: `color-mix(in srgb, ${accent} 10%, var(--color-deepsea))`,
                  }}
                >
                  <div className="flex min-w-0 items-center gap-3">
                    <span
                      aria-hidden
                      className="h-2.5 w-2.5 shrink-0 rounded-full"
                      style={{ background: accent }}
                    />
                    <div className="min-w-0">
                      <p className="truncate text-sm text-white">
                        {b.template_name || '(deleted template)'} →{' '}
                        {b.list_name || '(deleted list)'}
                      </p>
                      <p className="text-xs text-slate-400">
                        {new Date(b.created_at).toLocaleString()}
                      </p>
                    </div>
                  </div>
                  <div className="flex shrink-0 flex-col items-end gap-1">
                    <span
                      className="rounded-full px-2 py-0.5 font-mono text-[0.65rem] uppercase tracking-widest"
                      style={{
                        background: `color-mix(in srgb, ${accent} 18%, transparent)`,
                        color: accent,
                      }}
                    >
                      {b.status}
                    </span>
                    <span className="font-mono text-xs text-slate-400">
                      {b.sent}/{b.total}
                    </span>
                  </div>
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </section>
  );
}

// A small paper-plane glyph in the house line-drawing style (no imagery, no
// shadows), tinted to the card accent via currentColor.
function SendGlyph() {
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
      <path d="M21 3 3 10.5l7 2.5 2.5 7z" />
      <path d="M21 3 10 14" />
    </svg>
  );
}
