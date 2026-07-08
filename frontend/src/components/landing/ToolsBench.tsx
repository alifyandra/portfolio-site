'use client';

// "The bench": the tools grid. Each card reveals once as it scrolls in
// (disabled under prefers-reduced-motion). Card open/locked/soon state comes
// from the viewer's auth tier, read once here and passed down.

import { motion, useReducedMotion } from 'motion/react';
import { useAuth } from '@/lib/auth';
import { tools } from '@/lib/apps';
import { ToolCard } from './ToolCard';

export function ToolsBench() {
  const { isAuthenticated, isFriend, signIn } = useAuth();
  const reduce = useReducedMotion();

  return (
    <section
      id="tools"
      className="mx-auto w-full max-w-4xl scroll-mt-24 px-6 py-20"
    >
      <p className="font-mono text-sm lowercase tracking-wide text-mint">
        the bench
      </p>
      <h2 className="section-title mt-2 font-display text-3xl font-bold sm:text-4xl">
        Pick something off the bench.
      </h2>
      {/* small coloured squiggle so the heading doesn't feel bare */}
      <svg
        width="132"
        height="12"
        viewBox="0 0 132 12"
        fill="none"
        aria-hidden
        className="mt-2 text-mint"
      >
        <path
          d="M2 7c8-6 16-6 24 0s16 6 24 0 16-6 24 0 16 6 24 0 16-6 24 0"
          stroke="currentColor"
          strokeWidth="3"
          strokeLinecap="round"
        />
      </svg>
      <p className="mt-3 max-w-2xl text-slate-400">
        Little tools I have built or am building. Some are open to everyone,
        some are friends only, some are still brewing.
      </p>

      <div className="mt-10 grid grid-cols-1 gap-5 sm:grid-cols-2 lg:grid-cols-3">
        {tools.map((tool, i) => (
          <motion.div
            key={tool.slug}
            initial={reduce ? false : { opacity: 0, y: 20 }}
            whileInView={reduce ? undefined : { opacity: 1, y: 0 }}
            viewport={{ once: true, margin: '-80px' }}
            transition={{
              duration: 0.5,
              delay: i * 0.08,
              ease: [0.22, 1, 0.36, 1],
            }}
          >
            <ToolCard
              tool={tool}
              index={i}
              isAuthenticated={isAuthenticated}
              isFriend={isFriend}
              reduce={reduce}
              signIn={signIn}
            />
          </motion.div>
        ))}
      </div>
    </section>
  );
}
