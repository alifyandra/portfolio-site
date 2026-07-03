'use client';

import { useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { QRCodeSVG } from 'qrcode.react';

import {
  useListWaTemplates,
  useListWaLists,
  useListWaBatches,
  getListWaBatchesQueryKey,
} from '@/lib/api/generated';
import type { BatchDTO } from '@/lib/api/model';
import { streamBatch, type WaEvent } from '@/lib/wa-stream';

type Phase = 'idle' | 'starting' | 'linking' | 'running' | 'done' | 'error';

interface ProgressRow {
  phone: string;
  name?: string;
  status: 'sent' | 'skipped' | 'failed';
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
  const [counts, setCounts] = useState({ sent: 0, skipped: 0, failed: 0 });
  const [log, setLog] = useState<ProgressRow[]>([]);
  const [message, setMessage] = useState<string | null>(null);

  const abortRef = useRef<AbortController | null>(null);

  const selectedList = lists.find((l) => l.id === listId);
  const total = selectedList?.recipient_count ?? 0;
  const processed = counts.sent + counts.skipped + counts.failed;
  const active = phase === 'starting' || phase === 'linking' || phase === 'running';
  const canSend =
    !active && templateId !== '' && listId !== '' && total > 0 && dailyRemaining > 0;

  const invalidateBatches = () =>
    queryClient.invalidateQueries({ queryKey: getListWaBatchesQueryKey() });

  const reset = () => {
    setPhase('idle');
    setQr(null);
    setCounts({ sent: 0, skipped: 0, failed: 0 });
    setLog([]);
    setMessage(null);
  };

  const onEvent = (ev: WaEvent) => {
    switch (ev.type) {
      case 'qr':
        setQr(ev.value);
        setPhase('linking');
        break;
      case 'ready':
        setPhase('running');
        break;
      case 'progress':
        setLog((prev) => [...prev, ev].slice(-200));
        setCounts((prev) => ({
          sent: prev.sent + (ev.status === 'sent' ? 1 : 0),
          skipped: prev.skipped + (ev.status === 'skipped' ? 1 : 0),
          failed: prev.failed + (ev.status === 'failed' ? 1 : 0),
        }));
        break;
      case 'done':
        setCounts({ sent: ev.sent, skipped: ev.skipped, failed: ev.failed });
        setMessage(`Done. Sent ${ev.sent}, skipped ${ev.skipped}, failed ${ev.failed}.`);
        setPhase('done');
        break;
      case 'error':
        setMessage(ev.message);
        setPhase('error');
        break;
    }
  };

  const send = async () => {
    if (templateId === '' || listId === '') return;
    reset();
    setPhase('starting');
    const controller = new AbortController();
    abortRef.current = controller;
    try {
      await streamBatch({ template_id: templateId, list_id: listId }, onEvent, controller.signal);
    } catch (err) {
      if (controller.signal.aborted) {
        // User cancelled: fall back to idle rather than showing an error.
        setPhase('idle');
      } else {
        setMessage(err instanceof Error ? err.message : 'The send failed.');
        setPhase('error');
      }
    } finally {
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
          {phase === 'error' && message && (
            <p className="text-sm text-coral">{message}</p>
          )}

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

      {/* Running / done: progress */}
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

          {log.length > 0 && (
            <ul className="max-h-56 overflow-y-auto rounded-md border border-slate-800 bg-deepsea p-2 font-mono text-xs">
              {log.map((r, i) => (
                <li key={`${r.phone}-${i}`} className="flex justify-between gap-2 py-0.5">
                  <span className="text-slate-300">
                    {r.phone}
                    {r.name ? ` (${r.name})` : ''}
                  </span>
                  <span
                    className={
                      r.status === 'sent'
                        ? 'text-mint'
                        : r.status === 'skipped'
                          ? 'text-slate-500'
                          : 'text-coral'
                    }
                  >
                    {r.status}
                    {r.reason ? ` · ${r.reason}` : ''}
                  </span>
                </li>
              ))}
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
