'use client';

// Dependency-free inline-SVG area/line chart for one account's balance history
// (the codebase hand-rolls SVG throughout, e.g. the WhatsApp glyphs). No chart
// library is installed and none may be added. Theme-aware: strokes/fills use the
// palette CSS vars (which flip per theme), text uses the slate utilities.
//
// Two robustness rules, because the series can now be bucketed:
//
// Unplottable points are dropped BEFORE the value domain is computed. A single
// non-finite balance or unparseable timestamp anywhere in the array would
// otherwise make min/max NaN and blank the entire chart, and a bucketed series
// has more ways to carry one than a raw reading list did.
//
// The domain is found in a single pass rather than with Math.min(...balances).
// The spread form passes one argument per point, which also breaks on a long
// series, and a daily year across a deep account is a lot of arguments.

import { useMemo, useState } from 'react';

import type { BalancePointDTO } from '@/lib/api/model';
import { formatMoney, formatDate } from './format';

// viewBox coordinate space. The SVG scales to its container width via a 100%
// width + preserveAspectRatio; all geometry below is in these units.
const VB_W = 640;
const VB_H = 220;
const PAD = { top: 16, right: 16, bottom: 16, left: 16 };
const PLOT_W = VB_W - PAD.left - PAD.right;
const PLOT_H = VB_H - PAD.top - PAD.bottom;

interface Plotted {
  point: BalancePointDTO;
  x: number; // viewBox x
  y: number; // viewBox y
  fx: number; // fraction 0..1 across the plot (for hover mapping)
  carried: boolean; // bucket had no reading; repeats the previous close
}

export function BalanceChart({ points }: { points: BalancePointDTO[] }) {
  const [hover, setHover] = useState<number | null>(null);

  const model = useMemo(() => {
    const usable = (points ?? [])
      .map((point) => ({ point, t: new Date(point?.as_of ?? '').getTime() }))
      .filter(
        ({ point, t }) => Number.isFinite(point?.balance) && Number.isFinite(t),
      );
    if (usable.length === 0) return null;

    let min = Infinity;
    let max = -Infinity;
    let tMin = Infinity;
    let tMax = -Infinity;
    let minIdx = 0;
    let maxIdx = 0;
    usable.forEach(({ point, t }, i) => {
      if (point.balance < min) {
        min = point.balance;
        minIdx = i;
      }
      if (point.balance > max) {
        max = point.balance;
        maxIdx = i;
      }
      if (t < tMin) tMin = t;
      if (t > tMax) tMax = t;
    });

    if (min === max) {
      // Flat series: pad so the line sits mid-height rather than on an edge.
      const pad = Math.abs(min) * 0.05 || 1;
      min -= pad;
      max += pad;
    }
    const span = max - min;
    const tSpan = tMax - tMin || 1;

    const plotted: Plotted[] = usable.map(({ point, t }, i) => {
      const fx = usable.length === 1 ? 0.5 : (t - tMin) / tSpan;
      return {
        point,
        x: PAD.left + fx * PLOT_W,
        y: PAD.top + (1 - (point.balance - min) / span) * PLOT_H,
        fx,
        carried: point.carried === true,
      };
    });

    // The line is drawn as two overlaid paths so a carried stretch reads as "no
    // reading" rather than "no change". Without this the carry-forward rule is
    // invisible in the UI, which is the one real cost of carrying instead of
    // emitting a gap. A segment is dashed when either end is carried.
    const solid: string[] = [];
    const dashed: string[] = [];
    for (let i = 1; i < plotted.length; i++) {
      const a = plotted[i - 1];
      const b = plotted[i];
      const seg = `M ${a.x.toFixed(2)} ${a.y.toFixed(2)} L ${b.x.toFixed(2)} ${b.y.toFixed(2)}`;
      (a.carried || b.carried ? dashed : solid).push(seg);
    }
    // A single point has no segment, so it gets a dot to sit on instead.
    const singleton = plotted.length === 1 ? plotted[0] : null;

    const baseY = PAD.top + PLOT_H;
    const areaPath =
      `M ${plotted[0].x.toFixed(2)} ${baseY.toFixed(2)} ` +
      plotted.map((pt) => `L ${pt.x.toFixed(2)} ${pt.y.toFixed(2)}`).join(' ') +
      ` L ${plotted[plotted.length - 1].x.toFixed(2)} ${baseY.toFixed(2)} Z`;

    // A faint zero reference line, only when 0 falls inside the value range.
    const zeroY =
      min < 0 && max > 0 ? PAD.top + (1 - (0 - min) / span) * PLOT_H : null;

    return {
      plotted,
      solidPath: solid.join(' '),
      dashedPath: dashed.join(' '),
      singleton,
      areaPath,
      zeroY,
      last: usable[usable.length - 1].point,
      minPoint: usable[minIdx].point,
      maxPoint: usable[maxIdx].point,
      carriedCount: plotted.filter((pt) => pt.carried).length,
      dropped: (points?.length ?? 0) - usable.length,
    };
  }, [points]);

  if (!model) {
    return (
      <p className="py-8 text-center text-sm text-slate-400">
        No balance history for this account yet.
      </p>
    );
  }

  const {
    plotted,
    solidPath,
    dashedPath,
    singleton,
    areaPath,
    zeroY,
    last,
    minPoint,
    maxPoint,
    carriedCount,
    dropped,
  } = model;
  const active = hover != null ? (plotted[hover] ?? null) : null;

  const handleMove = (e: React.MouseEvent<SVGSVGElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    if (rect.width === 0) return;
    const frac = (e.clientX - rect.left) / rect.width;
    // Nearest point by horizontal fraction.
    let best = 0;
    let bestD = Infinity;
    plotted.forEach((pt, i) => {
      const d = Math.abs(pt.fx - frac);
      if (d < bestD) {
        bestD = d;
        best = i;
      }
    });
    setHover(best);
  };

  return (
    <div className="relative">
      <svg
        viewBox={`0 0 ${VB_W} ${VB_H}`}
        width="100%"
        preserveAspectRatio="none"
        role="img"
        aria-label="Account balance over time"
        className="block h-52 w-full touch-none"
        onMouseMove={handleMove}
        onMouseLeave={() => setHover(null)}
      >
        <defs>
          <linearGradient id="fin-area" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="var(--color-sky)" stopOpacity="0.28" />
            <stop offset="100%" stopColor="var(--color-sky)" stopOpacity="0" />
          </linearGradient>
        </defs>

        {zeroY != null && (
          <line
            x1={PAD.left}
            y1={zeroY}
            x2={VB_W - PAD.right}
            y2={zeroY}
            stroke="var(--color-slate-600)"
            strokeWidth={1}
            strokeDasharray="4 4"
            vectorEffect="non-scaling-stroke"
          />
        )}

        <path d={areaPath} fill="url(#fin-area)" />
        {solidPath !== '' && (
          <path
            d={solidPath}
            fill="none"
            stroke="var(--color-sky)"
            strokeWidth={2}
            strokeLinejoin="round"
            strokeLinecap="round"
            vectorEffect="non-scaling-stroke"
          />
        )}
        {dashedPath !== '' && (
          <path
            d={dashedPath}
            fill="none"
            stroke="var(--color-sky)"
            strokeWidth={2}
            strokeDasharray="4 4"
            strokeLinejoin="round"
            strokeLinecap="round"
            vectorEffect="non-scaling-stroke"
          />
        )}
        {singleton && (
          <circle
            cx={singleton.x}
            cy={singleton.y}
            r={3}
            fill="var(--color-sky)"
            vectorEffect="non-scaling-stroke"
          />
        )}

        {/* Hollow markers on carried buckets, so a flat stretch is legible as
            "no reading" without having to hover it. */}
        {plotted
          .filter((pt) => pt.carried)
          .map((pt, i) => (
            <circle
              key={`carried-${i}`}
              cx={pt.x}
              cy={pt.y}
              r={2.5}
              fill="var(--canvas)"
              stroke="var(--color-sky)"
              strokeWidth={1.5}
              vectorEffect="non-scaling-stroke"
            />
          ))}

        {active && (
          <>
            <line
              x1={active.x}
              y1={PAD.top}
              x2={active.x}
              y2={PAD.top + PLOT_H}
              stroke="var(--color-slate-500)"
              strokeWidth={1}
              vectorEffect="non-scaling-stroke"
            />
            <circle
              cx={active.x}
              cy={active.y}
              r={4}
              fill={active.carried ? 'var(--canvas)' : 'var(--color-sky)'}
              stroke={active.carried ? 'var(--color-sky)' : 'var(--canvas)'}
              strokeWidth={2}
              vectorEffect="non-scaling-stroke"
            />
          </>
        )}
      </svg>

      {active && (
        <div
          className="pointer-events-none absolute top-0 z-10 -translate-x-1/2 rounded-md border border-slate-700 bg-deepsea px-2 py-1 text-center text-xs shadow-sm"
          style={{ left: `${active.fx * 100}%` }}
        >
          <div className="font-semibold text-white">
            {formatMoney(active.point.balance)}
          </div>
          <div className="text-slate-400">{formatDate(active.point.as_of)}</div>
          {active.carried && (
            <div className="text-slate-500">carried forward</div>
          )}
        </div>
      )}

      {/* min / last / max readout under the plot */}
      <div className="mt-3 flex flex-wrap justify-between gap-3 text-xs">
        <span className="text-slate-400">
          Low{' '}
          <span className="font-medium text-slate-200">
            {formatMoney(minPoint.balance)}
          </span>{' '}
          · {formatDate(minPoint.as_of)}
        </span>
        <span className="text-slate-400">
          Latest{' '}
          <span className="font-semibold text-sky">
            {formatMoney(last.balance)}
          </span>{' '}
          · {formatDate(last.as_of)}
        </span>
        <span className="text-slate-400">
          High{' '}
          <span className="font-medium text-slate-200">
            {formatMoney(maxPoint.balance)}
          </span>{' '}
          · {formatDate(maxPoint.as_of)}
        </span>
      </div>

      {(carriedCount > 0 || dropped > 0) && (
        <p className="mt-1 text-xs text-slate-500">
          {carriedCount > 0 && (
            <>
              Dashed segments and hollow markers are buckets with no reading,
              repeating the previous close ({carriedCount} of {plotted.length}).
            </>
          )}
          {dropped > 0 && <> {dropped} unplottable point(s) skipped.</>}
        </p>
      )}
    </div>
  );
}
