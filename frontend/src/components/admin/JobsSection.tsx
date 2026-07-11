'use client';

// Jobs section of the Admin Console (ADR 0014, phase P6): control the ScheduledJob
// registry the in-process scheduler drives. Enable/disable, edit schedule/timezone/
// runner, force-start a run, read run history, and mint/revoke the scope-only bearer
// tokens external runners use. Writes go to /api/admin/jobs + /api/admin/tokens behind
// the server-enforced admin middleware; this UI is admin-gated for UX only. Transport
// is the session cookie (customFetch), never a bearer token in the browser.

import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';

import {
  useListJobs,
  useListJobKinds,
  useCreateJob,
  useUpdateJob,
  useStartJobRun,
  useListJobRuns,
  useListApiTokens,
  useMintApiToken,
  useRevokeApiToken,
  getListJobsQueryKey,
  getListJobRunsQueryKey,
  getListApiTokensQueryKey,
} from '@/lib/api/generated';
import type { JobDTO, JobRunDTO, ApiTokenDTO, JobKindDTO } from '@/lib/api/model';
import {
  UpdateJobInputBodyRunner,
  CreateJobInputBodyRunner,
} from '@/lib/api/model';
import {
  citronCard,
  citronBadge,
  inputClass,
  labelClass,
  selectClass,
  primaryBtn,
  ghostBtn,
  editBtn,
  dangerBtn,
} from './ui';
import { CronScheduleField } from './CronScheduleField';

// A small curated set of IANA timezones for the schedule dropdown — enough to cover
// where jobs actually run without a free-text field that invites typos. UTC first (the
// default and what prod uses), then Alif's local zone and a few common others. The
// backend still accepts any valid IANA name, so this is a convenience, not a limit.
const TIMEZONES = [
  'UTC',
  'Australia/Melbourne',
  'Australia/Sydney',
  'Asia/Jakarta',
  'Europe/London',
  'America/New_York',
];

// A run is "in flight" while its most recent status is queued or running; the
// force-start button is disabled then so an admin cannot pile up duplicate work
// (the server enforces the same guard with a 409).
const ACTIVE = new Set(['queued', 'running']);

const statusColor: Record<string, string> = {
  succeeded: 'text-mint',
  failed: 'text-coral',
  running: 'text-sky',
  queued: 'text-citron',
  cancelled: 'text-slate-400',
};

function formatDateTime(iso: string | undefined): string {
  if (!iso) return '—';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

function formatDuration(seconds: number | undefined): string {
  if (!seconds || seconds <= 0) return '—';
  if (seconds < 60) return `${seconds}s`;
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return s ? `${m}m ${s}s` : `${m}m`;
}

export function JobsSection() {
  const { data, isLoading } = useListJobs();
  const jobs = data?.jobs ?? [];

  return (
    <div className="flex flex-col gap-6">
      <section
        className="flex flex-col gap-5 rounded-2xl border p-5 sm:p-6"
        style={citronCard}
      >
        <header className="flex items-center gap-3">
          <span
            className="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-xl text-citron"
            style={citronBadge}
          >
            <JobsGlyph />
          </span>
          <div>
            <h2 className="font-display text-lg font-bold text-white">Jobs</h2>
            <p className="text-sm text-slate-400">
              Scheduled jobs the worker runs. Toggle, edit the schedule, force a
              run, or read the run history.
            </p>
          </div>
        </header>

        <CreateJobForm existingKeys={new Set(jobs.map((j) => j.key))} />

        {isLoading ? (
          <p className="text-sm text-slate-400">Loading…</p>
        ) : jobs.length === 0 ? (
          <p className="text-sm text-slate-400">No scheduled jobs registered.</p>
        ) : (
          <ul className="flex flex-col gap-3">
            {jobs.map((job) => (
              <JobRow key={job.id} job={job} />
            ))}
          </ul>
        )}
      </section>

      <TokensPanel />
    </div>
  );
}

// Register a new ScheduledJob row (ADR 0014). The job is picked from the backend
// job-kinds registry (GET /api/admin/job-kinds) rather than typed: choosing a kind
// derives its immutable key and stage and pre-fills a sensible name/schedule/timezone,
// so an admin never has to know what to type (and can't register a key the worker cannot
// dispatch). The schedule is built with CronScheduleField, not a raw cron string. Uses
// the generated cookie-transport hook, invalidates the list on success, and surfaces a
// 409 (duplicate) / 422 (bad cron or timezone) inline.
function CreateJobForm({ existingKeys }: { existingKeys: Set<string> }) {
  const queryClient = useQueryClient();
  const { data: kindsData, isLoading: kindsLoading } = useListJobKinds();
  const create = useCreateJob();

  const kinds: JobKindDTO[] = kindsData?.kinds ?? [];
  const available = kinds.filter((k) => !existingKeys.has(k.key));
  const allRegistered = kinds.length > 0 && available.length === 0;

  const [selectedKey, setSelectedKey] = useState('');
  const [name, setName] = useState('');
  const [schedule, setSchedule] = useState('');
  const [timezone, setTimezone] = useState('UTC');
  const [runner, setRunner] = useState<CreateJobInputBodyRunner>(
    CreateJobInputBodyRunner.server,
  );
  const [enabled, setEnabled] = useState(false);

  const selected = kinds.find((k) => k.key === selectedKey);

  const reset = () => {
    setSelectedKey('');
    setName('');
    setSchedule('');
    setTimezone('UTC');
    setRunner(CreateJobInputBodyRunner.server);
    setEnabled(false);
  };

  // Picking a kind pre-fills the editable fields from its registry defaults; key and
  // stage are derived from the kind (shown read-only), never typed.
  const pickKind = (key: string) => {
    setSelectedKey(key);
    const k = kinds.find((x) => x.key === key);
    if (k) {
      setName(k.name);
      setSchedule(k.default_schedule);
      setTimezone(k.default_timezone || 'UTC');
    }
  };

  const canCreate =
    selectedKey.length > 0 &&
    name.trim().length > 0 &&
    schedule.trim().length > 0 &&
    !create.isPending;

  const submit = () => {
    if (!canCreate) return;
    create.mutate(
      {
        data: {
          key: selectedKey,
          name: name.trim(),
          schedule: schedule.trim(),
          timezone: timezone || 'UTC',
          runner,
          enabled,
        },
      },
      {
        onSuccess: () => {
          queryClient.invalidateQueries({ queryKey: getListJobsQueryKey() });
          reset();
        },
      },
    );
  };

  // Keep the current timezone selectable even if it is not in the curated list.
  const tzOptions = Array.from(new Set([...TIMEZONES, timezone]));

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-slate-700 bg-deepsea/40 p-4">
      <p className="text-sm font-medium text-white">Add job</p>

      {kindsLoading ? (
        <p className="text-sm text-slate-400">Loading available jobs…</p>
      ) : kinds.length === 0 ? (
        <p className="text-sm text-slate-400">No job kinds are available.</p>
      ) : allRegistered ? (
        <p className="text-sm text-slate-400">
          All available jobs are already registered.
        </p>
      ) : (
        <>
          <label className={labelClass}>
            Job
            <select
              className={selectClass}
              value={selectedKey}
              onChange={(e) => pickKind(e.target.value)}
            >
              <option value="" disabled>
                Select a job…
              </option>
              {kinds.map((k) => {
                const added = existingKeys.has(k.key);
                return (
                  <option key={k.key} value={k.key} disabled={added}>
                    {k.name}
                    {added ? ' (added)' : ''}
                  </option>
                );
              })}
            </select>
          </label>

          {selected ? (
            <>
              <div className="flex flex-wrap items-center gap-2 text-xs">
                <span className="rounded bg-white/5 px-1.5 py-0.5 font-mono text-slate-400">
                  {selected.key}
                </span>
                <span className="rounded bg-white/5 px-1.5 py-0.5 font-mono text-slate-400">
                  {selected.stage}
                </span>
                {selected.description ? (
                  <span className="text-slate-500">{selected.description}</span>
                ) : null}
              </div>

              <div className="grid gap-3 sm:grid-cols-2">
                <label className={labelClass}>
                  Name
                  <input
                    type="text"
                    className={inputClass}
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                  />
                </label>
                <label className={labelClass}>
                  Timezone
                  <select
                    className={selectClass}
                    value={timezone}
                    onChange={(e) => setTimezone(e.target.value)}
                  >
                    {tzOptions.map((tz) => (
                      <option key={tz} value={tz}>
                        {tz}
                      </option>
                    ))}
                  </select>
                </label>
                <label className={labelClass}>
                  Runner
                  <select
                    className={selectClass}
                    value={runner}
                    onChange={(e) =>
                      setRunner(e.target.value as CreateJobInputBodyRunner)
                    }
                  >
                    <option value={CreateJobInputBodyRunner.server}>server</option>
                    <option value={CreateJobInputBodyRunner.local}>local</option>
                    <option value={CreateJobInputBodyRunner.any}>any</option>
                  </select>
                </label>
              </div>

              <CronScheduleField
                value={schedule}
                onChange={setSchedule}
                timezone={timezone}
              />

              <label className="flex items-center gap-2 text-sm text-slate-300">
                <input
                  type="checkbox"
                  checked={enabled}
                  onChange={(e) => setEnabled(e.target.checked)}
                />
                Enabled
              </label>

              <div className="flex flex-wrap items-center gap-3">
                <button
                  type="button"
                  className={primaryBtn}
                  disabled={!canCreate}
                  onClick={submit}
                >
                  {create.isPending ? 'Adding…' : 'Add job'}
                </button>
                {create.error ? (
                  <p className="text-sm text-coral">
                    {(create.error as Error).message}
                  </p>
                ) : null}
              </div>
            </>
          ) : null}
        </>
      )}
    </div>
  );
}

function JobRow({ job }: { job: JobDTO }) {
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [showHistory, setShowHistory] = useState(false);
  const [startNote, setStartNote] = useState<string | null>(null);

  const [schedule, setSchedule] = useState(job.schedule);
  const [timezone, setTimezone] = useState(job.timezone);
  const [runner, setRunner] = useState<UpdateJobInputBodyRunner>(
    job.runner as UpdateJobInputBodyRunner,
  );

  const invalidateJobs = () =>
    queryClient.invalidateQueries({ queryKey: getListJobsQueryKey() });

  const update = useUpdateJob();
  const start = useStartJobRun();

  const active = job.last_status ? ACTIVE.has(job.last_status) : false;
  const busy = update.isPending || start.isPending;

  const toggle = () =>
    update.mutate(
      { id: job.id, data: { enabled: !job.enabled } },
      { onSuccess: invalidateJobs },
    );

  const saveEdit = () =>
    update.mutate(
      { id: job.id, data: { schedule: schedule.trim(), timezone: timezone.trim(), runner } },
      {
        onSuccess: () => {
          invalidateJobs();
          setEditing(false);
        },
      },
    );

  const openEdit = () => {
    setSchedule(job.schedule);
    setTimezone(job.timezone);
    setRunner(job.runner as UpdateJobInputBodyRunner);
    setEditing(true);
  };

  const forceStart = () => {
    setStartNote(null);
    start.mutate(
      { id: job.id },
      {
        onSuccess: (res) => {
          invalidateJobs();
          queryClient.invalidateQueries({
            queryKey: getListJobRunsQueryKey(job.id),
          });
          // Graceful "no worker" degrade: the server returns started=false with a
          // message rather than an error when the queue is unconfigured.
          if (!res.started) setStartNote(res.message ?? 'Run was not started.');
        },
      },
    );
  };

  return (
    <li className="flex flex-col gap-3 rounded-lg border border-slate-700 bg-deepsea/40 p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <p className="truncate font-medium text-white">{job.name}</p>
            <span className="rounded bg-white/5 px-1.5 py-0.5 font-mono text-[11px] text-slate-400">
              {job.stage}
            </span>
          </div>
          <p className="mt-0.5 truncate font-mono text-xs text-slate-400">
            {job.key}
          </p>
        </div>
        <button
          type="button"
          onClick={toggle}
          disabled={busy}
          aria-pressed={job.enabled}
          className={`shrink-0 rounded-full px-3 py-1 text-xs font-semibold transition disabled:cursor-not-allowed disabled:opacity-50 ${
            job.enabled
              ? 'bg-mint/20 text-mint'
              : 'bg-slate-700/60 text-slate-300'
          }`}
        >
          {job.enabled ? 'Enabled' : 'Disabled'}
        </button>
      </div>

      {/* Facts */}
      <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs sm:grid-cols-4">
        <Fact label="Schedule" value={<span className="font-mono">{job.schedule}</span>} />
        <Fact label="Timezone" value={job.timezone} />
        <Fact label="Runner" value={job.runner} />
        <Fact
          label="Last status"
          value={
            job.last_status ? (
              <span className={statusColor[job.last_status] ?? 'text-slate-300'}>
                {job.last_status}
              </span>
            ) : (
              '—'
            )
          }
        />
        <Fact label="Next run" value={formatDateTime(job.next_run_at)} />
        <Fact label="Last run" value={formatDateTime(job.last_run_at)} />
      </dl>

      {/* Actions */}
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={forceStart}
          disabled={busy || active}
          className={editBtn}
          title={active ? 'A run is already queued or running' : undefined}
        >
          {start.isPending ? 'Starting…' : 'Run now'}
        </button>
        <button type="button" onClick={editing ? () => setEditing(false) : openEdit} className={ghostBtn}>
          {editing ? 'Cancel' : 'Edit schedule'}
        </button>
        <button
          type="button"
          onClick={() => setShowHistory((v) => !v)}
          className={ghostBtn}
        >
          {showHistory ? 'Hide history' : 'History'}
        </button>
      </div>

      {startNote ? <p className="text-xs text-amber-300">{startNote}</p> : null}
      {update.error ? (
        <p className="text-xs text-coral">{(update.error as Error).message}</p>
      ) : null}

      {/* Edit form */}
      {editing ? (
        <div className="flex flex-col gap-3 rounded-lg border border-slate-700 bg-deepsea/60 p-3">
          <CronScheduleField
            value={schedule}
            onChange={setSchedule}
            timezone={timezone}
          />
          <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
            <label className={`sm:w-52 ${labelClass}`}>
              Timezone
              <select
                className={selectClass}
                value={timezone}
                onChange={(e) => setTimezone(e.target.value)}
              >
                {Array.from(new Set([...TIMEZONES, timezone])).map((tz) => (
                  <option key={tz} value={tz}>
                    {tz}
                  </option>
                ))}
              </select>
            </label>
            <label className={`sm:w-36 ${labelClass}`}>
              Runner
              <select
                className={selectClass}
                value={runner}
                onChange={(e) =>
                  setRunner(e.target.value as UpdateJobInputBodyRunner)
                }
              >
                <option value={UpdateJobInputBodyRunner.server}>server</option>
                <option value={UpdateJobInputBodyRunner.local}>local</option>
                <option value={UpdateJobInputBodyRunner.any}>any</option>
              </select>
            </label>
            <button
              type="button"
              className={primaryBtn}
              disabled={update.isPending || schedule.trim().length === 0}
              onClick={saveEdit}
            >
              {update.isPending ? 'Saving…' : 'Save'}
            </button>
          </div>
        </div>
      ) : null}

      {showHistory ? <RunHistory jobId={job.id} /> : null}
    </li>
  );
}

function Fact({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex flex-col">
      <dt className="font-mono uppercase tracking-widest text-slate-500">
        {label}
      </dt>
      <dd className="truncate text-slate-200">{value}</dd>
    </div>
  );
}

function RunHistory({ jobId }: { jobId: number }) {
  const { data, isLoading } = useListJobRuns(jobId, { limit: 20, offset: 0 });
  const runs: JobRunDTO[] = data?.runs ?? [];

  if (isLoading) {
    return <p className="text-xs text-slate-400">Loading history…</p>;
  }
  if (runs.length === 0) {
    return <p className="text-xs text-slate-400">No runs yet.</p>;
  }

  return (
    <div className="overflow-x-auto rounded-lg border border-slate-700">
      <table className="w-full border-collapse text-left text-xs">
        <thead>
          <tr className="border-b border-slate-700 text-slate-400">
            <th className="px-3 py-2 font-medium">Status</th>
            <th className="px-3 py-2 font-medium">Trigger</th>
            <th className="px-3 py-2 font-medium">Runner</th>
            <th className="px-3 py-2 font-medium">Started</th>
            <th className="px-3 py-2 font-medium">Duration</th>
            <th className="px-3 py-2 font-medium">Detail</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((r) => (
            <tr key={r.id} className="border-b border-slate-800 last:border-0">
              <td className="px-3 py-2">
                <span className={statusColor[r.status] ?? 'text-slate-300'}>
                  {r.status}
                </span>
              </td>
              <td className="px-3 py-2 text-slate-300">{r.trigger}</td>
              <td className="px-3 py-2 text-slate-400">{r.runner || '—'}</td>
              <td className="px-3 py-2 text-slate-400">
                {formatDateTime(r.started_at)}
              </td>
              <td className="px-3 py-2 text-slate-400">
                {formatDuration(r.duration_seconds)}
              </td>
              <td className="px-3 py-2 text-slate-400">
                {r.error ? (
                  <span className="text-coral" title={r.error}>
                    {r.error.length > 60 ? `${r.error.slice(0, 60)}…` : r.error}
                  </span>
                ) : (
                  '—'
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function TokensPanel() {
  const queryClient = useQueryClient();
  const { data, isLoading } = useListApiTokens();
  const tokens: ApiTokenDTO[] = data?.tokens ?? [];

  const [name, setName] = useState('');
  const [runner, setRunner] = useState('');
  const [scope, setScope] = useState('');
  // The raw token is returned by the server exactly once at mint time and never
  // stored; hold it in local state so it can be copied before it is dismissed.
  const [minted, setMinted] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: getListApiTokensQueryKey() });

  const mint = useMintApiToken();
  const revoke = useRevokeApiToken();

  const canMint =
    name.trim().length > 0 && runner.trim().length > 0 && !mint.isPending;

  const doMint = () => {
    if (!canMint) return;
    const scopeList = scope
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
    mint.mutate(
      {
        data: { name: name.trim(), runner: runner.trim(), scope: scopeList },
      },
      {
        onSuccess: (res) => {
          invalidate();
          setMinted(res.token);
          setCopied(false);
          setName('');
          setRunner('');
          setScope('');
        },
      },
    );
  };

  const copy = async () => {
    if (!minted) return;
    try {
      await navigator.clipboard.writeText(minted);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  };

  const doRevoke = (t: ApiTokenDTO) => {
    if (!confirm(`Revoke token "${t.name}" (${t.runner})?`)) return;
    revoke.mutate({ id: t.id }, { onSuccess: invalidate });
  };

  return (
    <section
      className="flex flex-col gap-5 rounded-2xl border p-5 sm:p-6"
      style={citronCard}
    >
      <header className="flex items-center gap-3">
        <span
          className="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-xl text-citron"
          style={citronBadge}
        >
          <TokenGlyph />
        </span>
        <div>
          <h2 className="font-display text-lg font-bold text-white">
            Runner tokens
          </h2>
          <p className="text-sm text-slate-400">
            Scope-only bearer tokens for external runners. They never grant admin
            access.
          </p>
        </div>
      </header>

      {/* Mint form */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
        <label className={`flex-1 ${labelClass}`}>
          Name
          <input
            type="text"
            className={inputClass}
            placeholder="laptop Claude Code"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        <label className={`sm:w-40 ${labelClass}`}>
          Runner
          <input
            type="text"
            className={inputClass}
            placeholder="laptop"
            value={runner}
            onChange={(e) => setRunner(e.target.value)}
          />
        </label>
        <label className={`flex-1 ${labelClass}`}>
          Scope (comma-separated job keys)
          <input
            type="text"
            className={inputClass}
            placeholder="digest.llm"
            value={scope}
            onChange={(e) => setScope(e.target.value)}
          />
        </label>
        <button
          type="button"
          className={primaryBtn}
          disabled={!canMint}
          onClick={doMint}
        >
          {mint.isPending ? 'Minting…' : 'Mint'}
        </button>
      </div>

      {mint.error ? (
        <p className="text-sm text-coral">{(mint.error as Error).message}</p>
      ) : null}

      {/* Raw token, shown once */}
      {minted ? (
        <div
          className="flex flex-col gap-2 rounded-lg border p-3"
          style={{
            borderColor: 'color-mix(in srgb, var(--color-citron) 42%, transparent)',
            background: 'color-mix(in srgb, var(--color-citron) 10%, transparent)',
          }}
        >
          <p className="text-xs text-citron">
            Copy this token now. It is shown once and cannot be recovered.
          </p>
          <div className="flex items-center gap-2">
            <code className="min-w-0 flex-1 truncate rounded bg-deepsea px-2 py-1.5 font-mono text-xs text-white">
              {minted}
            </code>
            <button type="button" className={editBtn} onClick={copy}>
              {copied ? 'Copied' : 'Copy'}
            </button>
            <button
              type="button"
              className={ghostBtn}
              onClick={() => setMinted(null)}
            >
              Done
            </button>
          </div>
        </div>
      ) : null}

      {/* Token list */}
      {isLoading ? (
        <p className="text-sm text-slate-400">Loading…</p>
      ) : tokens.length === 0 ? (
        <p className="text-sm text-slate-400">No runner tokens yet.</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {tokens.map((t) => (
            <li
              key={t.id}
              className="flex items-start justify-between gap-4 rounded-lg border border-slate-700 bg-deepsea/40 p-3"
            >
              <div className="min-w-0">
                <p className="truncate font-medium text-white">{t.name}</p>
                <p className="mt-0.5 text-xs text-slate-400">
                  <span className="font-mono">{t.runner}</span>
                  {t.scope && t.scope.length > 0 ? (
                    <span> · scope: {t.scope.join(', ')}</span>
                  ) : (
                    <span> · no scope</span>
                  )}
                </p>
                <p className="mt-0.5 text-xs text-slate-500">
                  last used {formatDateTime(t.last_used_at)}
                  {t.expires_at ? ` · expires ${formatDateTime(t.expires_at)}` : ''}
                </p>
              </div>
              <button
                type="button"
                onClick={() => doRevoke(t)}
                disabled={revoke.isPending}
                className={`${dangerBtn} shrink-0`}
              >
                Revoke
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function JobsGlyph() {
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
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v5l3 2" />
    </svg>
  );
}

function TokenGlyph() {
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
      <circle cx="7.5" cy="15.5" r="4.5" />
      <path d="M10.5 12.5 20 3M17 6l2 2M14 9l2 2" />
    </svg>
  );
}
