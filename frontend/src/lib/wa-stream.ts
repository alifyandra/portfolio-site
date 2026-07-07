// Hand-written client for the WhatsApp batch send. The send streams the QR and
// per-recipient progress as newline-delimited JSON (see the sidecar contract in
// the backend repo), which orval's generated React Query hook cannot model, so
// this reads the response body directly. It also intentionally rides a POST:
// a send has side effects and must not be an EventSource GET that a proxy or
// reconnect could re-fire.

import { BASE_URL } from './fetcher';

export type WaEvent =
  // Fargate cold-start heartbeat: emitted zero or more times while the sidecar
  // boots, only before the first `qr`. `message` is a short human status; it
  // has no batch-count effect (like `waiting`). Never arrives in static/local
  // mode, where the QR appears straight away.
  | { type: 'provisioning'; message?: string }
  | { type: 'qr'; value: string }
  | { type: 'ready' }
  | {
      type: 'progress';
      phone: string;
      name?: string;
      status: 'sent' | 'skipped' | 'failed';
      reason?: string;
      error?: string;
    }
  | { type: 'waiting'; ms: number; next_phone?: string; next_name?: string }
  | { type: 'done'; sent: number; skipped: number; failed: number }
  | { type: 'error'; message: string };

/**
 * streamBatch opens POST /api/wa/batches and invokes onEvent for each streamed
 * event until the stream closes. It throws before streaming for a pre-stream
 * rejection (the friend gate, the caps, or validation), whose body is an RFC7807
 * problem JSON. Pass an AbortSignal to cancel: aborting closes the connection,
 * which the backend treats as the batch failing.
 */
export async function streamBatch(
  body: { template_id: number; list_id: number },
  onEvent: (ev: WaEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(`${BASE_URL}/api/wa/batches`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal,
  });

  if (!res.ok) {
    // Rejected before any streaming began (401/403/404/422/429/503). The body is
    // a problem+json document; surface its detail.
    const text = await res.text().catch(() => '');
    let message = `request failed (${res.status})`;
    try {
      const problem = JSON.parse(text) as { detail?: string; title?: string };
      message = problem.detail || problem.title || message;
    } catch {
      // non-JSON body: keep the generic message
    }
    throw new Error(message);
  }
  if (!res.body) {
    throw new Error('the server did not return a stream');
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  const flushLine = (line: string) => {
    const trimmed = line.trim();
    if (!trimmed) return;
    try {
      onEvent(JSON.parse(trimmed) as WaEvent);
    } catch {
      // ignore a malformed line rather than aborting the whole read
    }
  };

  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let idx: number;
    while ((idx = buffer.indexOf('\n')) >= 0) {
      flushLine(buffer.slice(0, idx));
      buffer = buffer.slice(idx + 1);
    }
  }
  flushLine(buffer);
}
