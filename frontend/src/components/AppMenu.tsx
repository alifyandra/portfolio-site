'use client';

// The app menu in the navbar: a button that opens a dropdown listing Tools
// grouped by Category (see CONTEXT.md), in the defined order with empty
// categories hidden. Each row shows the tool's name plus a small state hint:
//   open   — live + reachable by this viewer: a link to the tool.
//   locked — live + friend-only + not a friend. Logged out => the row signs
//            you in; a member => inert "friends only".
//   soon   — not built yet: inert, labelled "soon"/"brewing".
// State mirrors the bench card logic (ToolCard), computed here from useAuth().
// Access is UX only; the backend re-enforces the real gate on every request.

import { useCallback, useEffect, useId, useRef, useState } from 'react';
import Link from 'next/link';
import { motion, AnimatePresence, useReducedMotion } from 'motion/react';

import { useAuth } from '@/lib/auth';
import {
  toolsByCategory,
  categoryLabels,
  type Tool,
  type ToolAccent,
} from '@/lib/apps';

type RowKind = 'open' | 'locked-signin' | 'locked-member' | 'soon';

const accentVar: Record<ToolAccent, string> = {
  mint: 'var(--color-mint)',
  sky: 'var(--color-sky)',
  citron: 'var(--color-citron)',
  coral: 'var(--color-coral)',
};

function rowKind(tool: Tool, isAuthenticated: boolean, isFriend: boolean): RowKind {
  if (tool.status === 'soon') return 'soon';
  if (tool.access === 'public' || isFriend) return 'open';
  return isAuthenticated ? 'locked-member' : 'locked-signin';
}

function GridGlyph() {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <rect x="3" y="3" width="7" height="7" rx="1.5" />
      <rect x="14" y="3" width="7" height="7" rx="1.5" />
      <rect x="3" y="14" width="7" height="7" rx="1.5" />
      <rect x="14" y="14" width="7" height="7" rx="1.5" />
    </svg>
  );
}

function SmallLock() {
  return (
    <svg
      width="13"
      height="13"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <rect x="4" y="10.5" width="16" height="10" rx="2" />
      <path d="M8 10.5V7a4 4 0 0 1 8 0v3.5" />
    </svg>
  );
}

// Hint chip on the right of each row. accent tints the "open" hint only.
function Hint({ kind, tool }: { kind: RowKind; tool: Tool }) {
  if (kind === 'open') {
    return (
      <span
        className="font-mono text-[0.65rem] uppercase tracking-widest"
        style={{ color: accentVar[tool.accent] }}
      >
        open →
      </span>
    );
  }
  if (kind === 'soon') {
    const label = tool.name.toLowerCase().includes('brew') ? 'brewing' : 'soon';
    return (
      <span className="font-mono text-[0.65rem] uppercase tracking-widest text-slate-500">
        {label}
      </span>
    );
  }
  if (kind === 'locked-member') {
    return (
      <span className="inline-flex items-center gap-1 font-mono text-[0.65rem] uppercase tracking-widest text-slate-500">
        <SmallLock />
        friends only
      </span>
    );
  }
  // locked-signin
  return (
    <span className="inline-flex items-center gap-1 font-mono text-[0.65rem] uppercase tracking-widest text-slate-400">
      <SmallLock />
      sign in
    </span>
  );
}

const rowBase =
  'flex w-full items-center justify-between gap-4 rounded-lg px-2.5 py-2 text-left no-underline transition-colors';

export function AppMenu() {
  const { isAuthenticated, isFriend, signIn } = useAuth();
  const reduce = useReducedMotion();
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const itemRefs = useRef<(HTMLAnchorElement | HTMLButtonElement | null)[]>([]);
  const menuId = useId();

  const groups = toolsByCategory();
  // Flat list of the interactive (focusable) rows, in render order, for
  // arrow-key roving. soon + locked-member rows are inert and skipped.
  const flat: { tool: Tool; kind: RowKind }[] = [];
  for (const group of groups) {
    for (const tool of group.tools) {
      flat.push({ tool, kind: rowKind(tool, isAuthenticated, isFriend) });
    }
  }
  const focusable = flat
    .map((r, i) => ({ ...r, i }))
    .filter((r) => r.kind === 'open' || r.kind === 'locked-signin');

  const close = useCallback((returnFocus = true) => {
    setOpen(false);
    if (returnFocus) buttonRef.current?.focus();
  }, []);

  // Close on outside click / focus leaving the menu.
  useEffect(() => {
    if (!open) return;
    function onPointerDown(e: PointerEvent) {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener('pointerdown', onPointerDown);
    return () => document.removeEventListener('pointerdown', onPointerDown);
  }, [open]);

  // On open, move focus to the first focusable row.
  useEffect(() => {
    if (!open) return;
    const first = focusable[0]?.i;
    if (first != null) itemRefs.current[first]?.focus();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const focusByOrder = (order: number) => {
    if (focusable.length === 0) return;
    const wrapped = (order + focusable.length) % focusable.length;
    itemRefs.current[focusable[wrapped].i]?.focus();
  };

  const currentOrder = () => {
    const active = document.activeElement;
    return focusable.findIndex((r) => itemRefs.current[r.i] === active);
  };

  const onMenuKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      close();
      return;
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      focusByOrder(currentOrder() + 1);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      focusByOrder(currentOrder() - 1);
    } else if (e.key === 'Home') {
      e.preventDefault();
      focusByOrder(0);
    } else if (e.key === 'End') {
      e.preventDefault();
      focusByOrder(focusable.length - 1);
    }
  };

  return (
    <div ref={rootRef} className="relative">
      <motion.button
        ref={buttonRef}
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        whileTap={reduce ? undefined : { scale: 0.92 }}
        className={`flex items-center gap-1.5 rounded-md px-2 py-1 text-sm font-medium no-underline transition ${
          open ? 'bg-white/5 text-white' : 'text-slate-300 hover:text-white'
        }`}
      >
        {/* The grid glyph gives a playful quarter-turn nudge while the menu is
            open (skipped under reduced motion). */}
        <motion.span
          className="inline-flex"
          animate={reduce ? undefined : { rotate: open ? 90 : 0 }}
          transition={{ type: 'spring', stiffness: 500, damping: 30 }}
        >
          <GridGlyph />
        </motion.span>
        <span className="hidden sm:inline">Apps</span>
      </motion.button>

      <AnimatePresence>
        {open ? (
          <motion.div
            id={menuId}
            role="menu"
            aria-label="Tools"
            onKeyDown={onMenuKeyDown}
            initial={reduce ? { opacity: 0 } : { opacity: 0, y: -6, scale: 0.96 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={reduce ? { opacity: 0 } : { opacity: 0, y: -6, scale: 0.96 }}
            transition={
              reduce
                ? { duration: 0.12 }
                : { type: 'spring', stiffness: 420, damping: 28, mass: 0.7 }
            }
            className="absolute right-0 top-full z-50 mt-2 w-72 max-w-[calc(100vw-2rem)] overflow-hidden rounded-2xl border border-slate-700 bg-deepsea p-2"
            style={{
              transformOrigin: 'top right',
              boxShadow: '0 12px 40px -12px rgba(0,0,0,0.45)',
            }}
          >
          {groups.map((group, gi) => (
            <div key={group.category} className={gi > 0 ? 'mt-1.5' : undefined}>
              <p className="px-2.5 pb-1 pt-1.5 font-mono text-[0.65rem] uppercase tracking-widest text-slate-500">
                {categoryLabels[group.category]}
              </p>
              {group.tools.map((tool) => {
                const flatIndex = flat.findIndex((r) => r.tool === tool);
                const kind = flat[flatIndex].kind;
                const accent = accentVar[tool.accent];

                const label = (
                  <span className="flex min-w-0 items-center gap-2.5">
                    <span
                      aria-hidden
                      className="h-2 w-2 shrink-0 rounded-full"
                      style={{
                        background:
                          kind === 'open'
                            ? accent
                            : 'color-mix(in srgb, var(--color-slate-500) 60%, transparent)',
                      }}
                    />
                    <span
                      className={`truncate text-sm ${
                        kind === 'open'
                          ? 'font-medium text-white'
                          : 'text-slate-400'
                      }`}
                    >
                      {tool.name}
                    </span>
                  </span>
                );

                // Open: a link that navigates and closes the menu.
                if (kind === 'open' && tool.href) {
                  return (
                    <Link
                      key={tool.slug}
                      href={tool.href}
                      role="menuitem"
                      ref={(el) => {
                        itemRefs.current[flatIndex] = el;
                      }}
                      onClick={() => close(false)}
                      className={`${rowBase} text-white hover:bg-white/5`}
                    >
                      {label}
                      <Hint kind={kind} tool={tool} />
                    </Link>
                  );
                }

                // Locked + logged out: the row signs you in.
                if (kind === 'locked-signin') {
                  return (
                    <button
                      key={tool.slug}
                      type="button"
                      role="menuitem"
                      ref={(el) => {
                        itemRefs.current[flatIndex] = el;
                      }}
                      onClick={() => {
                        close(false);
                        signIn();
                      }}
                      className={`${rowBase} hover:bg-white/5`}
                    >
                      {label}
                      <Hint kind={kind} tool={tool} />
                    </button>
                  );
                }

                // Locked member / soon: inert, announced disabled.
                return (
                  <div
                    key={tool.slug}
                    role="menuitem"
                    aria-disabled
                    className={`${rowBase} cursor-default opacity-80`}
                  >
                    {label}
                    <Hint kind={kind} tool={tool} />
                  </div>
                );
              })}
            </div>
          ))}
        </motion.div>
      ) : null}
      </AnimatePresence>
    </div>
  );
}
