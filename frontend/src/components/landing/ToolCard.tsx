'use client';

// A single card on "the bench". State is derived from the tool's status/access
// and the viewer's auth tier:
//   open   — live + (public or friend viewer): vivid accent card, whole card
//            links to the tool, glyph drifts and card lifts on hover.
//   locked — live + friend-only + not a friend: frosted/desaturated + padlock.
//            logged out -> click signs in; member -> "friends only", inert.
//   soon   — not built yet: dashed ghost card, muted, not clickable.

import Link from 'next/link';
import { motion } from 'motion/react';
import type { Tool, ToolAccent } from '@/lib/apps';
import { Glyph } from './glyphs';

const accentVar: Record<ToolAccent, string> = {
  mint: 'var(--color-mint)',
  sky: 'var(--color-sky)',
  citron: 'var(--color-citron)',
  coral: 'var(--color-coral)',
};

const cardBase =
  'group relative flex h-full flex-col gap-4 overflow-hidden rounded-2xl border p-6';

function StatusTag({ label, color }: { label: string; color?: string }) {
  return (
    <span
      className="font-mono text-xs uppercase tracking-widest text-slate-400"
      style={color ? { color } : undefined}
    >
      {label}
    </span>
  );
}

function LockIcon({ className }: { className?: string }) {
  return (
    <svg
      width="40"
      height="40"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className={className}
    >
      <rect x="4" y="10.5" width="16" height="10" rx="2" />
      <path d="M8 10.5V7a4 4 0 0 1 8 0v3.5" />
    </svg>
  );
}

type CardProps = {
  tool: Tool;
  index: number;
  isAuthenticated: boolean;
  isFriend: boolean;
  reduce: boolean | null;
  signIn: () => void;
};

export function ToolCard({
  tool,
  index,
  isAuthenticated,
  isFriend,
  reduce,
  signIn,
}: CardProps) {
  const accent = accentVar[tool.accent];

  const isOpen =
    tool.status === 'live' && (tool.access === 'public' || isFriend);
  const isLocked =
    tool.status === 'live' && tool.access === 'friend' && !isFriend;

  // ── Open ────────────────────────────────────────────────────────────────
  if (isOpen && tool.href) {
    return (
      <motion.div
        whileHover={reduce ? undefined : { y: -4 }}
        transition={{ type: 'spring', stiffness: 320, damping: 22 }}
        className="h-full"
      >
        <Link
          href={tool.href}
          className={`${cardBase} no-underline`}
          style={{
            borderColor: `color-mix(in srgb, ${accent} 45%, transparent)`,
            background: `color-mix(in srgb, ${accent} 8%, var(--color-deepsea))`,
          }}
        >
          {/* soft glow on hover — no heavy drop-shadow */}
          <span
            aria-hidden
            className="pointer-events-none absolute -inset-px rounded-2xl opacity-0 transition-opacity duration-300 group-hover:opacity-100"
            style={{ boxShadow: `0 0 44px -10px ${accent}` }}
          />
          <div className="flex items-start justify-between">
            <span
              className="transition-transform duration-500 ease-out group-hover:-translate-y-1 group-hover:translate-x-1"
              style={{ color: accent }}
            >
              <Glyph index={index} />
            </span>
            <StatusTag label="live" color={accent} />
          </div>
          <div className="flex flex-col gap-1">
            <h3 className="font-display text-xl font-bold text-white">
              {tool.name}
            </h3>
            <p className="text-sm leading-relaxed text-slate-300">
              {tool.tagline}
            </p>
          </div>
          <span
            className="mt-auto inline-flex items-center gap-1 text-sm font-semibold transition-transform duration-300 group-hover:translate-x-1"
            style={{ color: accent }}
          >
            Open ↗
          </span>
        </Link>
      </motion.div>
    );
  }

  // ── Locked ──────────────────────────────────────────────────────────────
  if (isLocked) {
    const sublabel = isAuthenticated
      ? 'Friends only'
      : 'Sign in to check access';
    const cls = `${cardBase} border-slate-700 bg-deepsea/40 text-left backdrop-blur`;
    const inner = (
      <>
        <div className="flex items-start justify-between">
          <span className="text-slate-500">
            <LockIcon />
          </span>
          <StatusTag label="live" />
        </div>
        <div className="flex flex-col gap-1">
          <h3 className="font-display text-xl font-bold text-slate-300">
            {tool.name}
          </h3>
          <p className="text-sm leading-relaxed text-slate-400">
            {tool.tagline}
          </p>
        </div>
        <span className="mt-auto text-sm font-medium text-slate-400">
          {sublabel}
        </span>
      </>
    );

    // Logged out: the whole card signs you in. Member: inert.
    return isAuthenticated ? (
      <div className={cls}>{inner}</div>
    ) : (
      <button
        type="button"
        onClick={signIn}
        className={`${cls} transition-colors hover:border-slate-500`}
      >
        {inner}
      </button>
    );
  }

  // ── Soon ──────────────────────────────────────────────────────────────
  const soonTag = tool.name.toLowerCase().includes('brew') ? 'brewing' : 'soon';
  return (
    <div className={`${cardBase} border-dashed border-slate-600 bg-transparent`}>
      <div className="flex items-start justify-between">
        <span style={{ color: `color-mix(in srgb, ${accent} 55%, transparent)` }}>
          <Glyph index={index} />
        </span>
        <StatusTag label={soonTag} color={accent} />
      </div>
      <div className="flex flex-col gap-1">
        <h3 className="font-display text-xl font-bold text-slate-400">
          {tool.name}
        </h3>
        <p className="text-sm leading-relaxed text-slate-500">{tool.tagline}</p>
      </div>
      {tool.access === 'friend' ? (
        <span className="mt-auto font-mono text-xs uppercase tracking-widest text-slate-500">
          friends only
        </span>
      ) : (
        <span className="mt-auto text-sm text-slate-500">Not ready yet</span>
      )}
    </div>
  );
}
