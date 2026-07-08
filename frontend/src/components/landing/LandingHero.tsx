'use client';

// Landing hero: ambient drifting shapes behind, a mono eyebrow, the big
// two-colour aliflabs wordmark, a muted tagline, and an auth-aware CTA. The
// entrance is a gentle staggered fade+rise; under prefers-reduced-motion the
// content renders at its final state immediately (no animation).

import { motion, useReducedMotion } from 'motion/react';
import { useAuth } from '@/lib/auth';
import { BrandMark } from '@/components/BrandMark';
import { AmbientShapes } from './AmbientShapes';

const container = {
  hidden: {},
  show: { transition: { staggerChildren: 0.12, delayChildren: 0.05 } },
};

const item = {
  hidden: { opacity: 0, y: 16 },
  show: {
    opacity: 1,
    y: 0,
    transition: { duration: 0.5, ease: [0.22, 1, 0.36, 1] as const },
  },
};

export function LandingHero() {
  const { isLoading, isAuthenticated, signIn } = useAuth();
  const reduce = useReducedMotion();

  return (
    <section className="relative overflow-hidden">
      <AmbientShapes />
      <motion.div
        variants={container}
        initial={reduce ? false : 'hidden'}
        animate="show"
        className="mx-auto flex w-full max-w-4xl flex-col gap-6 px-6 pb-20 pt-20 sm:pb-28 sm:pt-28"
      >
        <motion.p
          variants={item}
          className="font-mono text-sm lowercase tracking-wide text-mint"
        >
          a workshop by alif · melbourne
        </motion.p>

        <motion.h1 variants={item}>
          {/* gentle continuous float on the wordmark (off under reduced motion) */}
          <motion.span
            className="inline-block"
            animate={reduce ? undefined : { y: [0, -8, 0] }}
            transition={
              reduce
                ? undefined
                : { duration: 6, repeat: Infinity, ease: 'easeInOut' }
            }
          >
            <BrandMark className="block text-6xl leading-none sm:text-7xl md:text-8xl" />
          </motion.span>
        </motion.h1>

        <motion.p
          variants={item}
          className="max-w-2xl text-lg leading-relaxed text-slate-300 sm:text-xl"
        >
          Small, sharp web tools. A few are live, more are on the bench. Sign in
          and the ones meant for you unlock.
        </motion.p>

        {!isLoading ? (
          <motion.div variants={item} className="mt-2 flex flex-wrap items-center gap-3">
            {isAuthenticated ? (
              <a
                href="#tools"
                className="rounded-md bg-citron px-5 py-2.5 font-semibold text-ink no-underline transition hover:brightness-95"
              >
                See your tools ↓
              </a>
            ) : (
              <>
                <button
                  type="button"
                  onClick={signIn}
                  className="rounded-md bg-citron px-5 py-2.5 font-semibold text-ink transition hover:brightness-95"
                >
                  Sign in with Google
                </button>
                <a
                  href="#tools"
                  className="rounded-md px-5 py-2.5 font-semibold text-slate-300 no-underline transition hover:text-white"
                >
                  Browse the bench ↓
                </a>
              </>
            )}
          </motion.div>
        ) : null}
      </motion.div>
    </section>
  );
}
