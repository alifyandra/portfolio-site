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
//
// Under basis=ledger a bucket carries a level (close) AND two sums (in, out).
// A level is continuous and a per-bucket sum is discrete, so they do not share
// a y-axis and a second axis on the same box would just invite reading a
// crossing that means nothing. Instead there are two stacked panels sharing one
// x-scale: the existing line for the level, and a shorter diverging bar strip
// beneath it for the flows. Both panels use the same viewBox width and the same
// horizontal padding and both are width:100% inside one container, so a bucket
// lands on the same screen x in both and one hover handler drives both.
//
// The per-bucket provenance signals (source, drift, flow_mismatch, a
// synthesized opening) stay out of the plot and live in the tooltip. Only
// `carried` is drawn, as it already was. A legend covering every way a number
// can be less trustworthy would be larger than the chart it annotates.
//
// Under the privacy censor every printed figure here is masked: the tooltip,
// the low/latest/high readouts and the tallest-bar caption. The geometry is
// not. A line and a bar strip carry shape and relative proportion, never
// digits, and both are drawn against a domain whose endpoints are themselves
// masked, so the censored chart still shows the trend without showing an
// amount. That is the point of keeping it.

import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react';

import type { BalancePointDTO } from '@/lib/api/model';
import { formatDate } from './format';
import { useMoney } from './censor';

// The tooltip has to be measured before it is painted, else an edge one shows
// for a frame at the position it is about to be moved away from. This page is
// prerendered, so fall back to useEffect where there is no layout to read.
const useMeasureEffect =
  typeof window === 'undefined' ? useEffect : useLayoutEffect;

// viewBox coordinate space. The SVG scales to its container width via a 100%
// width + preserveAspectRatio; all geometry below is in these units.
const VB_W = 640;
const VB_H = 220;
const PAD = { top: 16, right: 16, bottom: 16, left: 16 };
const PLOT_W = VB_W - PAD.left - PAD.right;
const PLOT_H = VB_H - PAD.top - PAD.bottom;

// The flow strip. Same VB_W and same left/right padding as the line panel, so
// an x in one panel is the same x in the other; only the height differs.
const FLOW_VB_H = 80;
const FLOW_PAD_Y = 10;
const FLOW_H = FLOW_VB_H - FLOW_PAD_Y * 2;
const FLOW_MID = FLOW_PAD_Y + FLOW_H / 2; // the zero line
const FLOW_HALF = FLOW_H / 2; // pixels available either side of zero

interface Plotted {
  point: BalancePointDTO;
  x: number; // viewBox x
  y: number; // viewBox y
  fx: number; // fraction 0..1 across the plot (for hover mapping)
  carried: boolean; // bucket had no reading; repeats the previous close
}

interface FlowBar {
  x: number; // viewBox x, shared with the line panel
  inH: number; // height above the zero line
  outH: number; // height below the zero line
}

/**
 * Ledger fields are absent from the DTO rather than null, so everything reading
 * one is a presence check. `x !== null` would pass on `undefined`.
 */
function isNum(value: number | undefined): value is number {
  return typeof value === 'number' && Number.isFinite(value);
}

/**
 * `in` and `out` are both reported as positive magnitudes; direction is carried
 * by which field it is, never by the sign. Clamp rather than take an absolute
 * value so a stray negative reads as "no flow" instead of quietly flipping a
 * bar to the wrong side of the zero line.
 */
function magnitude(value: number | undefined): number {
  return isNum(value) && value > 0 ? value : 0;
}

function sourceLabel(source: string): string {
  switch (source) {
    case 'balance_after':
      return "the bank's running balance";
    case 'accumulated':
      return 'walked back from a reading';
    case 'carried':
      return 'carried forward';
    default:
      return source;
  }
}

export function BalanceChart({
  points,
  basis,
}: {
  points: BalancePointDTO[];
  basis?: string;
}) {
  const { money } = useMoney();
  const [hover, setHover] = useState<number | null>(null);
  const ledger = basis === 'ledger';

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

    // Flow strip. The y-domain is symmetric around zero at the largest single
    // magnitude in the window, so a bar above and a bar below are the same
    // scale and can be compared by eye. Independent up/down domains would make
    // a small inflow look like a large one next to a large outflow.
    let flow: { bars: FlowBar[]; peak: number; barW: number } | null = null;
    if (ledger) {
      let peak = 0;
      for (const { point } of usable) {
        const i = magnitude(point.in);
        const o = magnitude(point.out);
        if (i > peak) peak = i;
        if (o > peak) peak = o;
      }
      // Bar width from the tightest gap between neighbouring buckets, so
      // months (28 to 31 days wide, hence not evenly spaced in time) still
      // never overlap.
      let minGap = PLOT_W;
      for (let i = 1; i < plotted.length; i++) {
        const gap = plotted[i].x - plotted[i - 1].x;
        if (gap > 0 && gap < minGap) minGap = gap;
      }
      const barW = Math.max(1.5, Math.min(20, minGap * 0.7));
      // A window where nothing moved is an answer, not a missing panel: a
      // dormant account over a short window has every bucket at zero. The strip
      // still draws its zero line and says so, because the section header has
      // already promised a flow panel below the line.
      const bars =
        peak > 0
          ? plotted.map((pt) => {
              const inV = magnitude(pt.point.in);
              const outV = magnitude(pt.point.out);
              return {
                x: pt.x,
                // A nonzero flow always gets at least a hairline, else a small
                // bucket next to a large one renders as nothing at all.
                inH: inV > 0 ? Math.max(1, (inV / peak) * FLOW_HALF) : 0,
                outH: outV > 0 ? Math.max(1, (outV / peak) * FLOW_HALF) : 0,
              };
            })
          : [];
      flow = { bars, peak, barW };
    }

    return {
      plotted,
      solidPath: solid.join(' '),
      dashedPath: dashed.join(' '),
      singleton,
      areaPath,
      zeroY,
      flow,
      last: usable[usable.length - 1].point,
      minPoint: usable[minIdx].point,
      maxPoint: usable[maxIdx].point,
      carriedCount: plotted.filter((pt) => pt.carried).length,
      dropped: (points?.length ?? 0) - usable.length,
    };
  }, [points, ledger]);

  // Tooltip containment. The box is centred on its bucket, but the ledger body
  // is wide enough to paint outside the card at either end, and there is no
  // clipping ancestor to save it. A percentage-of-container clamp cannot fix
  // that (the overflow is a function of the tooltip's own width), so measure
  // both and clamp the centre in pixels. Wherever the tooltip already fits, the
  // clamp returns exactly fx * containerWidth, which is the same place
  // `left: ${fx * 100}%` put it, so nothing moves except the boxes that were
  // hanging off the edge.
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const tipRef = useRef<HTMLDivElement | null>(null);
  const [tipLeft, setTipLeft] = useState<number | null>(null);
  const activeFx = hover != null ? (model?.plotted[hover]?.fx ?? null) : null;

  useMeasureEffect(() => {
    const wrap = wrapRef.current;
    const tip = tipRef.current;
    if (activeFx == null || !wrap || !tip) {
      setTipLeft(null);
      return;
    }
    const wrapW = wrap.getBoundingClientRect().width;
    const tipW = tip.getBoundingClientRect().width;
    if (wrapW === 0) return;
    const half = tipW / 2;
    setTipLeft(
      tipW >= wrapW
        ? wrapW / 2
        : Math.min(Math.max(activeFx * wrapW, half), wrapW - half),
    );
  }, [hover, activeFx]);

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
    flow,
    last,
    minPoint,
    maxPoint,
    carriedCount,
    dropped,
  } = model;
  const active = hover != null ? (plotted[hover] ?? null) : null;

  // Shared by both panels. They are the same width and the same left offset (a
  // 100%-width block each, in one container), so the same clientX maps to the
  // same fraction and both panels resolve the same bucket.
  //
  // The pointer fraction is taken across the full element but `fx` runs across
  // the padded plot, so the pointer is converted through the viewBox before the
  // two are compared. Skipping that conversion biases every lookup by
  // 0.025 - 0.05 * fx, which is more than half a bucket once the series passes
  // about 22 points, i.e. hovering one bar reports its neighbour on any daily
  // window. Both panels used to be wrong by the same amount, which made them
  // agree with each other and disagree with the chart.
  const handleMove = (e: React.MouseEvent<SVGSVGElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    if (rect.width === 0) return;
    const frac =
      (((e.clientX - rect.left) / rect.width) * VB_W - PAD.left) / PLOT_W;
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
    <div className="relative" ref={wrapRef}>
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

      {flow && (
        <>
          {/* The only encoding the tooltip cannot carry is which side is which,
              so that one sentence is the whole legend. */}
          <p className="mt-2 text-xs text-slate-500">
            {flow.peak > 0 ? (
              <>
                Per-bucket flow: <span className="text-mint">in</span> above the
                line, <span className="text-coral">out</span> below. Tallest bar{' '}
                {money(flow.peak)}.
              </>
            ) : (
              <>No money moved in or out over this window.</>
            )}
          </p>
          <svg
            viewBox={`0 0 ${VB_W} ${FLOW_VB_H}`}
            width="100%"
            preserveAspectRatio="none"
            role="img"
            aria-label="Money in and out per bucket"
            className="mt-1 block h-20 w-full"
            onMouseMove={handleMove}
            onMouseLeave={() => setHover(null)}
          >
            <line
              x1={PAD.left}
              y1={FLOW_MID}
              x2={VB_W - PAD.right}
              y2={FLOW_MID}
              stroke="var(--color-slate-600)"
              strokeWidth={1}
              strokeDasharray="4 4"
              vectorEffect="non-scaling-stroke"
            />
            {/* One highlight band behind the active bucket rather than dimming
                every other bar. Dimming rewrites an opacity on all N nodes on
                every mousemove, and N is in the hundreds on a daily year. */}
            {active && (
              <rect
                x={active.x - Math.max(flow.barW, 4)}
                y={FLOW_PAD_Y}
                width={Math.max(flow.barW, 4) * 2}
                height={FLOW_H}
                fill="var(--color-slate-600)"
                fillOpacity={0.22}
              />
            )}
            {flow.bars.map((bar, i) => {
              return (
                <g key={`flow-${i}`}>
                  {bar.inH > 0 && (
                    <rect
                      x={bar.x - flow.barW / 2}
                      y={FLOW_MID - bar.inH}
                      width={flow.barW}
                      height={bar.inH}
                      fill="var(--color-mint)"
                    />
                  )}
                  {bar.outH > 0 && (
                    <rect
                      x={bar.x - flow.barW / 2}
                      y={FLOW_MID}
                      width={flow.barW}
                      height={bar.outH}
                      fill="var(--color-coral)"
                    />
                  )}
                </g>
              );
            })}
            {active && (
              <line
                x1={active.x}
                y1={FLOW_PAD_Y}
                x2={active.x}
                y2={FLOW_VB_H - FLOW_PAD_Y}
                stroke="var(--color-slate-500)"
                strokeWidth={1}
                vectorEffect="non-scaling-stroke"
              />
            )}
          </svg>
        </>
      )}

      {active && (
        <div
          ref={tipRef}
          className="pointer-events-none absolute top-0 z-10 -translate-x-1/2 rounded-md border border-slate-700 bg-deepsea px-2 py-1 text-center text-xs shadow-sm"
          style={
            tipLeft != null
              ? { left: `${tipLeft}px` }
              : { left: `${active.fx * 100}%` }
          }
        >
          <div className="font-semibold text-white">
            {money(active.point.balance)}
          </div>
          <div className="text-slate-400">{formatDate(active.point.as_of)}</div>
          {active.carried && (
            <div className="text-slate-500">carried forward</div>
          )}
          {ledger && <LedgerDetail point={active.point} />}
        </div>
      )}

      {/* min / last / max readout under the plot */}
      <div className="mt-3 flex flex-wrap justify-between gap-3 text-xs">
        <span className="text-slate-400">
          Low{' '}
          <span className="font-medium text-slate-200">
            {money(minPoint.balance)}
          </span>{' '}
          · {formatDate(minPoint.as_of)}
        </span>
        <span className="text-slate-400">
          Latest{' '}
          <span className="font-semibold text-sky">
            {money(last.balance)}
          </span>{' '}
          · {formatDate(last.as_of)}
        </span>
        <span className="text-slate-400">
          High{' '}
          <span className="font-medium text-slate-200">
            {money(maxPoint.balance)}
          </span>{' '}
          · {formatDate(maxPoint.as_of)}
        </span>
      </div>

      {(carriedCount > 0 || dropped > 0) && (
        <p className="mt-1 text-xs text-slate-500">
          {carriedCount > 0 && (
            <>
              Dashed segments and hollow markers are buckets with{' '}
              {ledger ? 'no posted transactions' : 'no reading'}, repeating the
              previous close ({carriedCount} of {plotted.length}).
            </>
          )}
          {dropped > 0 && <> {dropped} unplottable point(s) skipped.</>}
        </p>
      )}
    </div>
  );
}

/**
 * The ledger provenance block inside the tooltip: everything the plot
 * deliberately does not draw. Every field here is optional on the DTO, so a
 * row only appears when the server actually sent the number.
 */
function LedgerDetail({ point }: { point: BalancePointDTO }) {
  const { money } = useMoney();
  const {
    open,
    close,
    in: moneyIn,
    out,
    net,
    external_in: extIn,
    external_out: extOut,
    txns,
    source,
    drift,
    flow_mismatch: mismatch,
  } = point;

  // Gross totals count transfers between the owner's own accounts; the external
  // pair does not. Showing both every time doubles the block for no gain, so
  // the external row only appears when the two actually disagree.
  const externalDiffers =
    (isNum(extIn) && isNum(moneyIn) && extIn !== moneyIn) ||
    (isNum(extOut) && isNum(out) && extOut !== out);

  const hasAny =
    isNum(open) ||
    isNum(close) ||
    isNum(moneyIn) ||
    isNum(out) ||
    isNum(net) ||
    isNum(txns) ||
    isNum(drift) ||
    typeof source === 'string' ||
    mismatch === true;
  if (!hasAny) return null;

  return (
    <div className="mt-1 space-y-0.5 border-t border-slate-700 pt-1 text-left">
      {isNum(open) && <TipRow label="Open" value={money(open)} />}
      {isNum(close) && <TipRow label="Close" value={money(close)} />}
      {/* Read through the same clamp the bars use. An absolute value would
          flip a stray negative into a positive figure the strip did not draw,
          so the text and the geometry would disagree about the same field. */}
      {isNum(moneyIn) && (
        <TipRow
          label="In"
          value={money(magnitude(moneyIn))}
          tone="text-mint"
        />
      )}
      {isNum(out) && (
        <TipRow
          label="Out"
          value={money(magnitude(out))}
          tone="text-coral"
        />
      )}
      {isNum(net) && <TipRow label="Net" value={money(net)} />}
      {externalDiffers && (
        <TipRow
          label="Excl. transfers"
          value={`${money(magnitude(extIn))} in · ${money(magnitude(extOut))} out`}
        />
      )}
      {isNum(txns) && <TipRow label="Rows" value={String(txns)} />}
      {typeof source === 'string' && source !== '' && (
        <TipRow label="From" value={sourceLabel(source)} />
      )}
      {isNum(drift) && <TipRow label="Drift" value={money(drift)} />}
      {mismatch === true && (
        <div className="text-coral">flows do not reconcile</div>
      )}
    </div>
  );
}

function TipRow({
  label,
  value,
  tone,
}: {
  label: string;
  value: string;
  tone?: string;
}) {
  return (
    <div className="flex justify-between gap-3 whitespace-nowrap">
      <span className="text-slate-500">{label}</span>
      <span className={tone ?? 'text-slate-200'}>{value}</span>
    </div>
  );
}
