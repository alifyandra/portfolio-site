'use client';

// Dependency-free inline-SVG area/line chart for one account's balance history
// (the codebase hand-rolls SVG throughout, e.g. the WhatsApp glyphs). No chart
// library is installed and none may be added. Theme-aware: strokes/fills use the
// palette CSS vars (which flip per theme), text uses the slate utilities.

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
}

export function BalanceChart({ points }: { points: BalancePointDTO[] }) {
  const [hover, setHover] = useState<number | null>(null);

  const model = useMemo(() => {
    if (points.length === 0) return null;

    const balances = points.map((p) => p.balance);
    let min = Math.min(...balances);
    let max = Math.max(...balances);
    if (min === max) {
      // Flat series: pad so the line sits mid-height rather than on an edge.
      const pad = Math.abs(min) * 0.05 || 1;
      min -= pad;
      max += pad;
    }
    const span = max - min;

    const times = points.map((p) => new Date(p.as_of).getTime());
    const tMin = Math.min(...times);
    const tMax = Math.max(...times);
    const tSpan = tMax - tMin || 1;

    const plotted: Plotted[] = points.map((p, i) => {
      const fx = points.length === 1 ? 0.5 : (times[i] - tMin) / tSpan;
      const x = PAD.left + fx * PLOT_W;
      const y = PAD.top + (1 - (p.balance - min) / span) * PLOT_H;
      return { point: p, x, y, fx };
    });

    const linePath = plotted
      .map((pt, i) => `${i === 0 ? 'M' : 'L'} ${pt.x.toFixed(2)} ${pt.y.toFixed(2)}`)
      .join(' ');

    const baseY = PAD.top + PLOT_H;
    const areaPath =
      `M ${plotted[0].x.toFixed(2)} ${baseY.toFixed(2)} ` +
      plotted.map((pt) => `L ${pt.x.toFixed(2)} ${pt.y.toFixed(2)}`).join(' ') +
      ` L ${plotted[plotted.length - 1].x.toFixed(2)} ${baseY.toFixed(2)} Z`;

    // A faint zero reference line, only when 0 falls inside the value range.
    const zeroY =
      min < 0 && max > 0
        ? PAD.top + (1 - (0 - min) / span) * PLOT_H
        : null;

    const last = points[points.length - 1];
    const minPoint = points[balances.indexOf(Math.min(...balances))];
    const maxPoint = points[balances.indexOf(Math.max(...balances))];

    return { plotted, linePath, areaPath, zeroY, last, minPoint, maxPoint };
  }, [points]);

  if (!model) {
    return (
      <p className="py-8 text-center text-sm text-slate-400">
        No balance snapshots for this account yet.
      </p>
    );
  }

  const { plotted, linePath, areaPath, zeroY, last, minPoint, maxPoint } = model;
  const active = hover != null ? plotted[hover] : null;

  const handleMove = (e: React.MouseEvent<SVGSVGElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
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
        <path
          d={linePath}
          fill="none"
          stroke="var(--color-sky)"
          strokeWidth={2}
          strokeLinejoin="round"
          strokeLinecap="round"
          vectorEffect="non-scaling-stroke"
        />

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
              fill="var(--color-sky)"
              stroke="var(--canvas)"
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
    </div>
  );
}
