'use client';

// Privacy censor for the finance surface: one switch that replaces every
// currency figure with a fixed mask, so the dashboard can be open on a laptop
// in a cafe without the numbers being readable from the next table.
//
// This is a shoulder-surfing control, not an access control. Every
// /api/finance/* read is already behind the server-enforced admin middleware,
// and the real figures are still in the JSON in the browser's memory. All this
// does is keep them off the glass.
//
// Three rules the implementation has to hold:
//
// 1. It starts censored on every page load and the state is deliberately NOT
//    persisted (no localStorage, no sessionStorage, no cookie, no server
//    field). Persisting it would mean that turning the figures on at home
//    leaves them on when the laptop is next opened in public, which is the
//    exact failure this exists to prevent. The provider sits in the root
//    Providers tree, so the choice survives client-side navigation between
//    /finance and /admin but not a reload, which is the intended lifetime.
//
// 2. A real figure must never paint before the censor applies. That falls out
//    of the default being `true`: the first render, server or client, is
//    already censored, and the only thing that reveals is a click. Nothing may
//    read a stored preference in an effect and then reveal, because that is a
//    revealed frame after the first paint.
//
// 3. The mask is one constant string for every figure. A shape derived from
//    the value (a bullet per digit, say) would leak the magnitude, which
//    matters most on the net-worth headline. It also carries no sign: a
//    censored negative must not give itself away with a minus, a bracket or a
//    colour, so call sites that tint by sign have to fall back to a neutral
//    tone while censored.

import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from 'react';

import { formatMoney, formatAbs } from './format';

/**
 * The one mask. Constant regardless of the value behind it, so nothing about
 * the magnitude, the sign or the digit count survives.
 *
 * The mask is shorter than most real figures, so toggling changes the width of
 * the text. Where a figure sits on its own line, in a grid cell, or in a
 * right-aligned `shrink-0` column, that only resizes its own box and nothing
 * around it moves. Where a figure sits inline with text after it, it does move
 * the rest of the line, so those sites wrap it in a `FigureSlot` that reserves
 * a fixed width instead. See FIGURE_CH.
 */
export const CENSOR_MASK = '$••••••';

/**
 * Reserved inline width, in `ch`, for a figure that has text after it on the
 * same line. Sized to hold a long AUD amount including its separators and a
 * sign, which is comfortably wider than the mask, so the text after it stays
 * put across a toggle. A figure wider than the reservation would still nudge
 * the line, but nothing on this surface is expected to reach that.
 */
export const FIGURE_CH = 11;

/**
 * Reserves a fixed inline width for one figure, so switching between the mask
 * and the real value does not move the words after it or change where the line
 * wraps. Purely cosmetic: the reservation is the same for every value, so it
 * carries no information about the figure inside it.
 */
export function FigureSlot({
  ch = FIGURE_CH,
  className = '',
  children,
}: {
  ch?: number;
  className?: string;
  children: ReactNode;
}) {
  return (
    <span
      className={`inline-block ${className}`}
      style={{ minWidth: `${ch}ch` }}
    >
      {children}
    </span>
  );
}

interface CensorContextValue {
  censored: boolean;
  toggle: () => void;
}

const CensorContext = createContext<CensorContextValue | null>(null);

export function CensorProvider({ children }: { children: ReactNode }) {
  // Censored by default, and there is no code path that changes this on mount.
  // See rule 2 above before adding one.
  const [censored, setCensored] = useState(true);
  const toggle = useCallback(() => setCensored((c) => !c), []);
  const value = useMemo(() => ({ censored, toggle }), [censored, toggle]);

  return (
    <CensorContext.Provider value={value}>{children}</CensorContext.Provider>
  );
}

/**
 * Censor-aware currency formatters, plus the flag itself for call sites that
 * also have to neutralise a sign-derived colour or a leading "+".
 *
 * Every currency render on the finance surface goes through here rather than
 * calling the pure formatters in ./format directly, which is what makes a
 * missed site greppable: outside this file, `formatMoney` / `formatAbs` should
 * have no callers.
 *
 * Falling back to censored when there is no provider is deliberate: a figure
 * rendered outside the tree fails closed, not open.
 */
export function useMoney() {
  const ctx = useContext(CensorContext);
  const censored = ctx?.censored ?? true;

  return useMemo(
    () => ({
      censored,
      /** Signed figure, masked while censored. */
      money: (value: number) => (censored ? CENSOR_MASK : formatMoney(value)),
      /** Magnitude only, masked while censored. */
      abs: (value: number) => (censored ? CENSOR_MASK : formatAbs(value)),
    }),
    [censored],
  );
}

/** Just the flag and the switch, for the toggle control and for wrappers. */
export function useCensor(): CensorContextValue {
  const ctx = useContext(CensorContext);
  return ctx ?? { censored: true, toggle: () => {} };
}

// Same tinted-surface idiom as citronBadge, kept local so the toggle carries
// the tint only in its pressed state.
const MASKED_TINT = 'color-mix(in srgb, var(--color-citron) 14%, transparent)';

/**
 * The control. Lives in the finance dashboard header and in the admin Wishlist
 * header (the other place a censored amount renders), both driving the same
 * state.
 *
 * State is legible three ways and never by colour alone: the glyph swaps
 * between an open and a struck-through eye, the text reads "Hidden" or
 * "Shown", and `aria-pressed` carries it for assistive tech.
 */
export function CensorToggle({ className = '' }: { className?: string }) {
  const { censored, toggle } = useCensor();

  return (
    <button
      type="button"
      onClick={toggle}
      aria-pressed={censored}
      aria-label={
        censored ? 'Show money figures' : 'Hide money figures from view'
      }
      title={
        censored
          ? 'Money figures are hidden. Click to show them.'
          : 'Money figures are visible. Click to hide them.'
      }
      className={`inline-flex shrink-0 items-center gap-2 rounded-lg border border-slate-700 px-3 py-1.5 text-xs font-medium transition hover:border-slate-500 ${
        censored ? 'text-citron' : 'text-slate-300 hover:text-white'
      } ${className}`}
      style={censored ? { background: MASKED_TINT } : undefined}
    >
      {censored ? <EyeOffGlyph /> : <EyeGlyph />}
      <span>{censored ? 'Amounts hidden' : 'Amounts shown'}</span>
    </button>
  );
}

/**
 * An amount input that stays masked while the censor is on, with a reveal on
 * the field itself.
 *
 * A plain input would undo the whole feature at the worst moment: opening a
 * bill or a wishlist row to fix a match pattern or a note would put its amount
 * on screen at text size, in a field the owner never asked to reveal. So while
 * censored the control renders as a read-only mask (the real value is not in
 * the DOM at all, not merely styled away) and the eye beside it reveals just
 * this one field. Deliberately editing the amount costs one extra click, which
 * is the right way round.
 *
 * Blocking the editor while censored was the other option and is worse: it
 * makes every non-amount field unreachable.
 *
 * The per-field reveal is local state, never persisted, and it is dropped
 * again the moment the global switch goes back to censored.
 */
export function AmountField({
  value,
  onChange,
  fieldLabel,
  className = '',
  step,
  min,
  placeholder,
}: {
  value: string;
  onChange: (next: string) => void;
  /** Names this field in the reveal button's accessible label. */
  fieldLabel: string;
  className?: string;
  step?: string;
  min?: string;
  placeholder?: string;
}) {
  const { censored } = useCensor();
  const [revealed, setRevealed] = useState(false);
  const [censoredWhenSet, setCensoredWhenSet] = useState(censored);

  // Turning the global switch back on has to re-hide a field that was revealed
  // individually, and it has to do it in the same pass, before anything paints.
  // This is the render-phase state adjustment React documents for exactly this
  // case, not an effect: an effect runs after the commit, which is one painted
  // frame of the real figure.
  let showValue = revealed;
  if (censored !== censoredWhenSet) {
    setCensoredWhenSet(censored);
    if (censored) {
      setRevealed(false);
      showValue = false;
    }
  }

  const hidden = censored && !showValue;

  return (
    <span className="flex w-full items-center gap-2">
      {hidden ? (
        <span className={`${className} min-w-0 flex-1 text-slate-400`}>
          {CENSOR_MASK}
        </span>
      ) : (
        <input
          type="number"
          step={step}
          min={min}
          placeholder={placeholder}
          className={`${className} min-w-0 flex-1`}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
      {censored && (
        <button
          type="button"
          onClick={() => setRevealed(hidden)}
          aria-pressed={hidden}
          aria-label={
            hidden
              ? `Show the ${fieldLabel} so it can be edited`
              : `Hide the ${fieldLabel} again`
          }
          title={
            hidden
              ? `The ${fieldLabel} is hidden. Click to edit it.`
              : `Click to hide the ${fieldLabel} again.`
          }
          className={`shrink-0 rounded-lg border border-slate-700 p-2 transition hover:border-slate-500 ${
            hidden ? 'text-citron' : 'text-slate-300 hover:text-white'
          }`}
          style={hidden ? { background: MASKED_TINT } : undefined}
        >
          {hidden ? <EyeOffGlyph /> : <EyeGlyph />}
        </button>
      )}
    </span>
  );
}

function EyeGlyph() {
  return (
    <svg
      width="15"
      height="15"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.9"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M2 12s3.6-6.5 10-6.5S22 12 22 12s-3.6 6.5-10 6.5S2 12 2 12Z" />
      <circle cx="12" cy="12" r="2.75" />
    </svg>
  );
}

function EyeOffGlyph() {
  return (
    <svg
      width="15"
      height="15"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.9"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M10.6 6.1A9.9 9.9 0 0 1 12 5.5c6.4 0 10 6.5 10 6.5a18 18 0 0 1-3.1 4M6.4 7.6A17.8 17.8 0 0 0 2 12s3.6 6.5 10 6.5a10.2 10.2 0 0 0 4-.8" />
      <path d="M9.9 9.9a3 3 0 0 0 4.2 4.2M3 3l18 18" />
    </svg>
  );
}
