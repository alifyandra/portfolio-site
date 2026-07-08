'use client';

// The WhatsApp Sender tool (ADR 11), gated to the friend and admin tiers. The
// gate here is UX only; the backend re-enforces it on every request (a member
// who reached this page still gets 403s). See ADR 10.

import { motion, useReducedMotion } from 'motion/react';
import { useAuth } from '@/lib/auth';
import { TemplatesPanel } from '@/components/whatsapp/TemplatesPanel';
import { ListsPanel } from '@/components/whatsapp/ListsPanel';
import { SendPanel } from '@/components/whatsapp/SendPanel';
import { CountryCodeSetting } from '@/components/whatsapp/CountryCodeSetting';

// Soft palette-tinted card surfaces (structural tints, safe in both themes:
// relative to --color-deepsea, which flips per theme). Foreground colours use
// the text-* utilities so they pick up the light-theme readability overrides.
const citronCard = {
  borderColor: 'color-mix(in srgb, var(--color-citron) 42%, transparent)',
  background: 'color-mix(in srgb, var(--color-citron) 9%, var(--color-deepsea))',
};
const coralCard = {
  borderColor: 'color-mix(in srgb, var(--color-coral) 42%, transparent)',
  background: 'color-mix(in srgb, var(--color-coral) 8%, var(--color-deepsea))',
};
const citronBadge = {
  background: 'color-mix(in srgb, var(--color-citron) 16%, transparent)',
};
const coralBadge = {
  background: 'color-mix(in srgb, var(--color-coral) 16%, transparent)',
};

const fade = {
  hidden: { opacity: 0, y: 16 },
  show: {
    opacity: 1,
    y: 0,
    transition: { duration: 0.5, ease: [0.22, 1, 0.36, 1] as const },
  },
};
const stagger = {
  hidden: {},
  show: { transition: { staggerChildren: 0.1, delayChildren: 0.05 } },
};

export default function WhatsAppPage() {
  const { isLoading, isAuthenticated, isFriend, signIn } = useAuth();
  const reduce = useReducedMotion();

  return (
    <main className="mx-auto flex min-h-screen w-full max-w-2xl flex-col gap-10 px-6 py-16">
      <motion.header
        variants={fade}
        initial={reduce ? false : 'hidden'}
        animate="show"
        className="flex flex-col gap-3"
      >
        <p className="font-mono text-sm lowercase tracking-wide text-mint">
          aliflabs · friends only
        </p>
        <h1 className="font-display text-3xl font-bold sm:text-4xl">
          <span className="text-white">WhatsApp </span>
          <span className="text-mint">Sender</span>
        </h1>
        {/* small coloured squiggle so the heading doesn't feel bare */}
        <svg
          width="120"
          height="11"
          viewBox="0 0 132 12"
          fill="none"
          aria-hidden
          className="text-mint"
        >
          <path
            d="M2 7c8-6 16-6 24 0s16 6 24 0 16-6 24 0 16 6 24 0 16-6 24 0"
            stroke="currentColor"
            strokeWidth="3"
            strokeLinecap="round"
          />
        </svg>
        <p className="max-w-xl leading-relaxed text-slate-300">
          Link your own WhatsApp, save a message template and a list of numbers,
          then send a personalized batch. Capped at 250 recipients and 3 batches a
          day.
        </p>
      </motion.header>

      {isLoading ? (
        <div className="flex items-center gap-2 text-sm text-slate-400">
          <span className="h-2 w-2 animate-pulse rounded-full bg-mint" />
          Loading…
        </div>
      ) : !isAuthenticated ? (
        <div
          className="flex flex-col items-start gap-4 rounded-2xl border p-6 sm:p-8"
          style={citronCard}
        >
          <span
            className="inline-flex h-11 w-11 items-center justify-center rounded-xl text-citron"
            style={citronBadge}
          >
            <svg
              width="22"
              height="22"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.9"
              strokeLinecap="round"
              strokeLinejoin="round"
              aria-hidden
            >
              <circle cx="9" cy="9" r="5" />
              <path d="M12.6 12.6 20 20M16.5 16.5l2-2M18.5 18.5l2-2" />
            </svg>
          </span>
          <div className="flex flex-col gap-1">
            <h2 className="font-display text-xl font-bold text-white">
              Sign in to get going
            </h2>
            <p className="text-slate-300">Sign in to use this tool.</p>
          </div>
          <button
            type="button"
            onClick={signIn}
            className="rounded-lg bg-citron px-5 py-2.5 font-semibold text-ink transition hover:brightness-95"
          >
            Sign in with Google
          </button>
        </div>
      ) : !isFriend ? (
        <div
          className="flex flex-col items-start gap-3 rounded-2xl border p-6"
          style={coralCard}
        >
          <span
            className="inline-flex h-11 w-11 items-center justify-center rounded-xl text-coral"
            style={coralBadge}
          >
            <svg
              width="22"
              height="22"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.9"
              strokeLinecap="round"
              strokeLinejoin="round"
              aria-hidden
            >
              <rect x="4" y="10.5" width="16" height="10" rx="2" />
              <path d="M8 10.5V7a4 4 0 0 1 8 0v3.5" />
            </svg>
          </span>
          <p className="font-mono text-xs uppercase tracking-widest text-coral">
            friends only
          </p>
          <p className="text-slate-300">
            This tool is available to friends only. If you think you should have
            access, get in touch.
          </p>
        </div>
      ) : (
        <motion.div
          variants={stagger}
          initial={reduce ? false : 'hidden'}
          animate="show"
          className="flex flex-col gap-6"
        >
          <motion.div variants={fade}>
            <SendPanel />
          </motion.div>
          <motion.div variants={fade}>
            <TemplatesPanel />
          </motion.div>
          <motion.div variants={fade} className="flex flex-col gap-4">
            <CountryCodeSetting />
            <ListsPanel />
          </motion.div>
        </motion.div>
      )}
    </main>
  );
}
