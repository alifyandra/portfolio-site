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

type Phase = 'idle' | 'starting' | 'linking' | 'running' | 'done' | 'error';
type RowStatus = 'pending' | 'sent' | 'skipped' | 'failed';

interface Row {
  phone: string;
  name?: string;
  status: RowStatus;
  reason?: string;
  error?: string;
}

const statusColor: Record<string, string> = {
  completed: 'text-mint',
  failed: 'text-coral',
  running: 'text-sky',
  linking: 'text-sky',
  pending: 'text-slate-400',
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
  const active = phase === 'starting' || phase === 'linking' || phase === 'running';
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
    <section className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold text-citron">Send</h2>
        <span className="text-sm text-slate-400">
          {dailyRemaining} send{dailyRemaining === 1 ? '' : 's'} left today
        </span>
      </div>

      {/* Configure + trigger */}
      {!active && phase !== 'done' && (
        <div className="flex flex-col gap-3 rounded-md border border-slate-700 bg-deepsea/60 p-4">
          <label className="flex flex-col gap-1 text-sm text-slate-300">
            Template
            <select
              className="rounded-md border border-slate-700 bg-deepsea px-3 py-2 text-white outline-none focus:border-sky"
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
              className="rounded-md border border-slate-700 bg-deepsea px-3 py-2 text-white outline-none focus:border-sky"
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
            className="self-start rounded-md bg-citron px-5 py-2 font-semibold text-deepsea transition hover:brightness-95 disabled:opacity-50"
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
        <div className="flex flex-col items-center gap-3 rounded-md border border-slate-700 bg-deepsea/60 p-6">
          <p className="text-sm text-slate-300">
            Scan with WhatsApp → Settings → Linked Devices
          </p>
          <div className="inline-block rounded-md bg-white p-3">
            <QRCodeSVG value={qr} size={220} />
          </div>
          <button
            type="button"
            onClick={cancel}
            className="rounded-md border border-slate-700 px-4 py-1.5 text-sm text-white transition hover:border-coral hover:text-coral"
          >
            Cancel
          </button>
        </div>
      )}

      {phase === 'starting' && (
        <p className="rounded-md border border-slate-700 bg-deepsea/60 p-4 text-sm text-slate-300">
          Starting a session…
        </p>
      )}

      {/* Running / done: full recipient list with live status + countdown */}
      {(phase === 'running' || phase === 'done') && (
        <div className="flex flex-col gap-3 rounded-md border border-slate-700 bg-deepsea/60 p-4">
          <div className="flex items-center justify-between">
            <span className="text-sm text-slate-300">
              {phase === 'running' ? 'Sending…' : 'Complete'}
            </span>
            <span className="text-sm text-slate-400">
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
            <p className="rounded-md border border-sky/40 bg-sky/10 px-3 py-2 text-sm text-sky">
              Next message in {countdown}s{nextLabel ? ` → ${nextLabel}` : ''}
            </p>
          )}
          {phase === 'running' && countdown === null && nextPhone && (
            <p className="rounded-md border border-sky/40 bg-sky/10 px-3 py-2 text-sm text-sky">
              Sending to {nextLabel}…
            </p>
          )}

          {rows.length > 0 && (
            <ul className="max-h-72 overflow-y-auto rounded-md border border-slate-800 bg-deepsea p-2 font-mono text-xs">
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
              className="self-start rounded-md border border-slate-700 px-4 py-1.5 text-sm text-white transition hover:border-coral hover:text-coral"
            >
              Cancel
            </button>
          )}
          {phase === 'done' && (
            <button
              type="button"
              onClick={reset}
              className="self-start rounded-md border border-slate-700 px-4 py-1.5 text-sm text-white transition hover:border-citron hover:text-citron"
            >
              Send another
            </button>
          )}
        </div>
      )}

      {/* History */}
      {batches.length > 0 && (
        <div className="mt-2 flex flex-col gap-2">
          <h3 className="text-sm font-semibold text-slate-300">Recent batches</h3>
          <ul className="flex flex-col gap-1.5">
            {batches.map((b: BatchDTO) => (
              <li
                key={b.id}
                className="flex items-center justify-between gap-3 rounded-md border border-slate-800 px-3 py-2 text-sm"
              >
                <div className="min-w-0">
                  <span className="text-slate-300">
                    {b.template_name || '(deleted template)'} →{' '}
                    {b.list_name || '(deleted list)'}
                  </span>
                  <span className="ml-2 text-xs text-slate-500">
                    {new Date(b.created_at).toLocaleString()}
                  </span>
                </div>
                <div className="flex shrink-0 items-center gap-3 text-xs">
                  <span className="text-slate-400">
                    {b.sent}/{b.total}
                  </span>
                  <span className={statusColor[b.status] ?? 'text-slate-400'}>
                    {b.status}
                  </span>
                </div>
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}
