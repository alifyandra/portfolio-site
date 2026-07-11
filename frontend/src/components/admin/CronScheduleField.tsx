'use client';

// A friendly builder for the standard 5-field cron string a ScheduledJob runs on
// (ADR 0014). The raw expression (e.g. "0 18 * * *") is easy to get wrong, so this
// offers presets — Every day / week / hour / N minutes — with a time picker and a
// day-of-week select, plus a plain-English summary and the raw cron for transparency.
// "Custom (cron)" is the escape hatch: a raw text input for anything the presets do not
// cover. Fully controlled: it derives its UI from `value` on every render (so a parent
// seeding a new value — e.g. picking a job kind — flows straight through) and emits the
// rebuilt cron via onChange. It mirrors the field names robfig/cron ParseStandard
// accepts, so what the picker builds is exactly what the backend validates and runs.

import { inputClass, labelClass, selectClass } from './ui';

type Mode = 'daily' | 'weekly' | 'hourly' | 'minutes' | 'custom';

interface Parsed {
  mode: Mode;
  minute: number; // 0-59
  hour: number; // 0-23
  dow: number; // 0-6 (0 = Sunday)
  interval: number; // minutes, for the "every N minutes" preset
}

const DEFAULTS: Omit<Parsed, 'mode'> = {
  minute: 0,
  hour: 9,
  dow: 1, // Monday
  interval: 15,
};

const DOW_NAMES = [
  'Sunday',
  'Monday',
  'Tuesday',
  'Wednesday',
  'Thursday',
  'Friday',
  'Saturday',
];

const INTERVAL_OPTIONS = [1, 2, 5, 10, 15, 20, 30];

// isInt matches a bare non-negative integer field (no ranges, lists or steps), so only
// the exact preset shapes below are recognised; anything richer falls through to custom.
function isInt(s: string): boolean {
  return /^\d+$/.test(s);
}

// parseCron does a best-effort match of `value` against the preset shapes. Anything it
// does not recognise (ranges, lists, multiple non-wildcard fields) is reported as
// custom, so the raw expression is preserved and shown in the escape-hatch input.
export function parseCron(value: string): Parsed {
  const base: Parsed = { mode: 'custom', ...DEFAULTS };
  const f = value.trim().split(/\s+/);
  if (f.length !== 5) return base;
  const [min, hr, dom, mon, dow] = f;

  // Every N minutes: "*/N * * * *"
  const stepMatch = /^\*\/(\d+)$/.exec(min);
  if (stepMatch && hr === '*' && dom === '*' && mon === '*' && dow === '*') {
    return { ...base, mode: 'minutes', interval: Number(stepMatch[1]) || DEFAULTS.interval };
  }

  if (dom === '*' && mon === '*') {
    // Hourly at minute M: "M * * * *"
    if (isInt(min) && hr === '*' && dow === '*') {
      return { ...base, mode: 'hourly', minute: Number(min) };
    }
    // Weekly on day D at H:M: "M H * * D"
    if (isInt(min) && isInt(hr) && isInt(dow)) {
      return {
        ...base,
        mode: 'weekly',
        minute: Number(min),
        hour: Number(hr),
        dow: Number(dow) % 7,
      };
    }
    // Daily at H:M: "M H * * *"
    if (isInt(min) && isInt(hr) && dow === '*') {
      return { ...base, mode: 'daily', minute: Number(min), hour: Number(hr) };
    }
  }
  return base;
}

// build assembles the cron string for a parsed state (custom returns the raw value the
// caller already holds, so build is never used for custom).
function build(p: Parsed): string {
  switch (p.mode) {
    case 'daily':
      return `${p.minute} ${p.hour} * * *`;
    case 'weekly':
      return `${p.minute} ${p.hour} * * ${p.dow}`;
    case 'hourly':
      return `${p.minute} * * * *`;
    case 'minutes':
      return `*/${p.interval} * * * *`;
    default:
      return '';
  }
}

const pad = (n: number) => String(n).padStart(2, '0');

// describe renders the plain-English summary shown under the controls.
function describe(p: Parsed, value: string, timezone?: string): string {
  const tz = timezone ? ` (${timezone})` : '';
  const at = `${pad(p.hour)}:${pad(p.minute)}`;
  switch (p.mode) {
    case 'daily':
      return `Every day at ${at}${tz}`;
    case 'weekly':
      return `Every ${DOW_NAMES[p.dow]} at ${at}${tz}`;
    case 'hourly':
      return `Every hour at :${pad(p.minute)}${tz}`;
    case 'minutes':
      return p.interval === 1 ? 'Every minute' : `Every ${p.interval} minutes`;
    default:
      return value.trim() ? 'Custom schedule' : 'No schedule set';
  }
}

export function CronScheduleField({
  value,
  onChange,
  timezone,
  label = 'Schedule',
}: {
  value: string;
  onChange: (cron: string) => void;
  timezone?: string;
  label?: string;
}) {
  const p = parseCron(value);

  // Switching frequency reuses the current hour/minute where it makes sense, so moving
  // Daily -> Weekly keeps the time the user already set. Custom emits the raw value
  // unchanged (the raw input below owns it).
  const setMode = (mode: Mode) => {
    if (mode === 'custom') {
      onChange(value); // keep whatever is there; the raw input takes over
      return;
    }
    onChange(build({ ...p, mode }));
  };

  const patch = (next: Partial<Parsed>) => onChange(build({ ...p, ...next }));

  return (
    <div className="flex flex-col gap-2">
      <span className="text-sm text-slate-300">{label}</span>
      <div className="flex flex-wrap items-end gap-3">
        <label className={`${labelClass} w-full sm:w-44`}>
          Frequency
          <select
            className={selectClass}
            value={p.mode}
            onChange={(e) => setMode(e.target.value as Mode)}
          >
            <option value="daily">Every day</option>
            <option value="weekly">Every week</option>
            <option value="hourly">Every hour</option>
            <option value="minutes">Every N minutes</option>
            <option value="custom">Custom (cron)</option>
          </select>
        </label>

        {p.mode === 'weekly' ? (
          <label className={`${labelClass} w-full sm:w-40`}>
            Day
            <select
              className={selectClass}
              value={p.dow}
              onChange={(e) => patch({ dow: Number(e.target.value) })}
            >
              {DOW_NAMES.map((name, i) => (
                <option key={name} value={i}>
                  {name}
                </option>
              ))}
            </select>
          </label>
        ) : null}

        {p.mode === 'daily' || p.mode === 'weekly' ? (
          <label className={`${labelClass} w-full sm:w-32`}>
            Time
            <input
              type="time"
              className={inputClass}
              value={`${pad(p.hour)}:${pad(p.minute)}`}
              onChange={(e) => {
                const [h, m] = e.target.value.split(':');
                patch({ hour: Number(h) || 0, minute: Number(m) || 0 });
              }}
            />
          </label>
        ) : null}

        {p.mode === 'hourly' ? (
          <label className={`${labelClass} w-full sm:w-40`}>
            At minute
            <select
              className={selectClass}
              value={p.minute}
              onChange={(e) => patch({ minute: Number(e.target.value) })}
            >
              {Array.from({ length: 60 }, (_, i) => (
                <option key={i} value={i}>
                  :{pad(i)}
                </option>
              ))}
            </select>
          </label>
        ) : null}

        {p.mode === 'minutes' ? (
          <label className={`${labelClass} w-full sm:w-40`}>
            Interval
            <select
              className={selectClass}
              value={p.interval}
              onChange={(e) => patch({ interval: Number(e.target.value) })}
            >
              {INTERVAL_OPTIONS.map((n) => (
                <option key={n} value={n}>
                  every {n} min
                </option>
              ))}
            </select>
          </label>
        ) : null}

        {p.mode === 'custom' ? (
          <label className={`${labelClass} flex-1`}>
            Cron expression
            <input
              type="text"
              className={`${inputClass} font-mono`}
              placeholder="0 18 * * *"
              value={value}
              onChange={(e) => onChange(e.target.value)}
            />
          </label>
        ) : null}
      </div>

      <p className="text-xs text-slate-400">
        {describe(p, value, timezone)}
        {value.trim() ? (
          <span className="ml-2 font-mono text-slate-500">cron: {value.trim()}</span>
        ) : null}
      </p>
    </div>
  );
}
