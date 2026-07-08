'use client';

// Ambient layer behind the hero: a bolder multi-stop palette gradient wash plus
// a curated set of distinct cute vector shapes (blobs, rings, dots, an arc, a
// wave, a plus, a triangle, a sparkle) drifting in the margins and around the
// wordmark. Everything references the palette CSS vars so it adapts to both
// themes; a per-theme opacity on .hero-ambient keeps it richer on the dark
// canvas and softer on the light one (see globals.css).
//
// Motion rules:
//   - each shape bobs / drifts / gently rotates on its own slow loop;
//   - a subtle pointer parallax nudges the shapes by depth;
//   - ALL of it is disabled when useReducedMotion() is true (shapes sit still),
//     and the parallax is additionally disabled on coarse/touch pointers.

import {
  motion,
  useMotionValue,
  useReducedMotion,
  useSpring,
  useTransform,
  type MotionValue,
  type TargetAndTransition,
} from 'motion/react';
import { useEffect, type CSSProperties } from 'react';

type ShapeKind =
  | 'blob'
  | 'ring'
  | 'dot'
  | 'arc'
  | 'wave'
  | 'plus'
  | 'triangle'
  | 'sparkle';

type ShapeConfig = {
  kind: ShapeKind;
  color: string;
  size: number;
  opacity: number;
  blur?: boolean;
  /** hidden below sm to keep 360px mobile uncluttered */
  desktopOnly?: boolean;
  pos: CSSProperties;
  /** px of pointer-parallax travel; larger reads as "closer" */
  depth: number;
  duration: number;
  delay: number;
  animate: TargetAndTransition;
};

// Palette references (adapt per theme). The neutral uses slate-500, the closest
// thing to a "deepsea tint" that stays visible on both the dark and light bg.
const MINT = 'var(--color-mint)';
const SKY = 'var(--color-sky)';
const CITRON = 'var(--color-citron)';
const CORAL = 'var(--color-coral)';
const NEUTRAL = 'var(--color-slate-500)';

// Big soft blurred blobs supply the colour field; the crisp small shapes are
// the recognizable accents, kept in the margins and up around the wordmark
// (never at high opacity behind the tagline/CTA band).
const shapes: ShapeConfig[] = [
  {
    kind: 'blob',
    color: MINT,
    size: 320,
    opacity: 0.3,
    blur: true,
    pos: { top: '-4rem', left: '-3rem' },
    depth: 16,
    duration: 15,
    delay: 0,
    animate: { x: [0, 24, -12, 0], y: [0, -18, 12, 0], scale: [1, 1.08, 0.96, 1] },
  },
  {
    kind: 'blob',
    color: CORAL,
    size: 280,
    opacity: 0.24,
    blur: true,
    pos: { bottom: '-5rem', right: '-3rem' },
    depth: 24,
    duration: 17,
    delay: 1.2,
    animate: { x: [0, -22, 12, 0], y: [0, 16, -10, 0], scale: [1, 1.06, 0.95, 1] },
  },
  {
    kind: 'blob',
    color: SKY,
    size: 240,
    opacity: 0.22,
    blur: true,
    pos: { top: '34%', right: '10%' },
    depth: 20,
    duration: 16,
    delay: 0.6,
    animate: { x: [0, 18, -14, 0], y: [0, -14, 10, 0], scale: [1, 1.05, 0.97, 1] },
  },
  {
    kind: 'ring',
    color: SKY,
    size: 66,
    opacity: 0.5,
    pos: { top: '13%', right: '7%' },
    depth: 42,
    duration: 11,
    delay: 0.2,
    animate: { y: [0, -16, 0], x: [0, 6, 0], rotate: [0, 10, 0] },
  },
  {
    // Desktop-only: on mobile the arc clipped the left edge of the tagline.
    kind: 'arc',
    color: CITRON,
    size: 82,
    opacity: 0.5,
    desktopOnly: true,
    pos: { top: '46%', left: '-1.25rem' },
    depth: 30,
    duration: 13,
    delay: 0.9,
    animate: { rotate: [0, -14, 0], y: [0, 12, 0] },
  },
  {
    // Desktop-only: on mobile the triangle crossed the "Browse the bench" link.
    kind: 'triangle',
    color: CORAL,
    size: 56,
    opacity: 0.48,
    desktopOnly: true,
    pos: { bottom: '14%', left: '5%' },
    depth: 34,
    duration: 14,
    delay: 1.6,
    animate: { rotate: [0, 14, 0], y: [0, -12, 0] },
  },
  {
    kind: 'dot',
    color: NEUTRAL,
    size: 16,
    opacity: 0.45,
    pos: { bottom: '22%', right: '16%' },
    depth: 50,
    duration: 9,
    delay: 0.4,
    animate: { y: [0, -12, 0], x: [0, -8, 0] },
  },
  // Around the wordmark — small, higher-depth, hidden on mobile to avoid crowd.
  {
    kind: 'sparkle',
    color: CITRON,
    size: 40,
    opacity: 0.65,
    desktopOnly: true,
    pos: { top: '15%', left: '39%' },
    depth: 58,
    duration: 8,
    delay: 0.3,
    animate: { scale: [1, 1.22, 1], rotate: [0, 20, 0] },
  },
  {
    kind: 'dot',
    color: CITRON,
    size: 18,
    opacity: 0.6,
    desktopOnly: true,
    pos: { top: '24%', left: '54%' },
    depth: 62,
    duration: 7,
    delay: 0.8,
    animate: { y: [0, -12, 0], x: [0, 8, 0] },
  },
  {
    kind: 'plus',
    color: MINT,
    size: 34,
    opacity: 0.55,
    desktopOnly: true,
    pos: { top: '9%', right: '24%' },
    depth: 48,
    duration: 10,
    delay: 1.1,
    animate: { rotate: [0, 18, 0], y: [0, -10, 0] },
  },
  {
    kind: 'wave',
    color: SKY,
    size: 92,
    opacity: 0.42,
    desktopOnly: true,
    pos: { top: '6%', left: '25%' },
    depth: 26,
    duration: 12,
    delay: 1.4,
    animate: { x: [0, 14, 0], y: [0, -8, 0] },
  },
];

function ShapeSvg({ kind, size }: { kind: ShapeKind; size: number }) {
  const common = {
    width: size,
    height: size,
    viewBox: '0 0 100 100',
    'aria-hidden': true,
  } as const;
  switch (kind) {
    case 'blob':
      return (
        <svg {...common}>
          <path
            fill="currentColor"
            d="M53 6c15-3 33 6 39 21s0 32-11 43-31 20-45 12S9 55 12 38 38 9 53 6z"
          />
        </svg>
      );
    case 'ring':
      return (
        <svg {...common}>
          <circle
            cx="50"
            cy="50"
            r="42"
            fill="none"
            stroke="currentColor"
            strokeWidth="7"
          />
        </svg>
      );
    case 'dot':
      return (
        <svg {...common}>
          <circle cx="50" cy="50" r="44" fill="currentColor" />
        </svg>
      );
    case 'arc':
      return (
        <svg {...common}>
          <path
            d="M12 82A46 46 0 0 1 88 30"
            fill="none"
            stroke="currentColor"
            strokeWidth="8"
            strokeLinecap="round"
          />
        </svg>
      );
    case 'wave':
      return (
        <svg {...common}>
          <path
            d="M6 55Q28 22 50 55T94 55"
            fill="none"
            stroke="currentColor"
            strokeWidth="8"
            strokeLinecap="round"
          />
        </svg>
      );
    case 'plus':
      return (
        <svg {...common}>
          <path
            d="M50 14V86M14 50H86"
            fill="none"
            stroke="currentColor"
            strokeWidth="12"
            strokeLinecap="round"
          />
        </svg>
      );
    case 'triangle':
      return (
        <svg {...common}>
          <path
            d="M50 14 86 84H14Z"
            fill="none"
            stroke="currentColor"
            strokeWidth="8"
            strokeLinejoin="round"
          />
        </svg>
      );
    case 'sparkle':
      return (
        <svg {...common}>
          <path
            fill="currentColor"
            d="M50 6c4 30 14 40 44 44-30 4-40 14-44 44-4-30-14-40-44-44 30-4 40-14 44-44z"
          />
        </svg>
      );
  }
}

function Shape({
  cfg,
  px,
  py,
  reduce,
}: {
  cfg: ShapeConfig;
  px: MotionValue<number>;
  py: MotionValue<number>;
  reduce: boolean | null;
}) {
  // The parallax offset scales the shared pointer value by this shape's depth.
  // px/py only ever move when the pointer listener is attached (fine pointer,
  // motion allowed); otherwise they sit at 0, so this is a no-op translate.
  const tx = useTransform(px, (v) => v * cfg.depth);
  const ty = useTransform(py, (v) => v * cfg.depth);

  return (
    <motion.div
      className={`absolute ${cfg.desktopOnly ? 'hidden sm:block' : ''}`}
      style={{ ...cfg.pos, x: tx, y: ty }}
    >
      <motion.div
        className={cfg.blur ? 'blur-2xl' : undefined}
        style={{ color: cfg.color, opacity: cfg.opacity }}
        animate={reduce ? undefined : cfg.animate}
        transition={
          reduce
            ? undefined
            : {
                duration: cfg.duration,
                delay: cfg.delay,
                repeat: Infinity,
                repeatType: 'loop',
                ease: 'easeInOut',
              }
        }
      >
        <ShapeSvg kind={cfg.kind} size={cfg.size} />
      </motion.div>
    </motion.div>
  );
}

export function AmbientShapes() {
  const reduce = useReducedMotion();

  // Normalized pointer offset from viewport centre (-1..1), spring-smoothed.
  const px = useMotionValue(0);
  const py = useMotionValue(0);
  const sx = useSpring(px, { stiffness: 50, damping: 18 });
  const sy = useSpring(py, { stiffness: 50, damping: 18 });

  useEffect(() => {
    if (reduce) return;
    // Only on fine pointers (mouse/trackpad); skip touch/coarse to avoid jank.
    if (!window.matchMedia('(pointer: fine)').matches) return;
    const onMove = (e: PointerEvent) => {
      px.set((e.clientX / window.innerWidth - 0.5) * 2);
      py.set((e.clientY / window.innerHeight - 0.5) * 2);
    };
    window.addEventListener('pointermove', onMove, { passive: true });
    return () => window.removeEventListener('pointermove', onMove);
  }, [reduce, px, py]);

  return (
    <div
      aria-hidden
      className="hero-ambient pointer-events-none absolute inset-0 -z-10 overflow-hidden"
    >
      {/* bolder multi-stop gradient wash */}
      <div
        className="absolute inset-0"
        style={{
          background: [
            'radial-gradient(55% 55% at 18% 12%, color-mix(in srgb, var(--color-mint) 26%, transparent), transparent 70%)',
            'radial-gradient(55% 55% at 86% 18%, color-mix(in srgb, var(--color-sky) 24%, transparent), transparent 70%)',
            'radial-gradient(60% 60% at 82% 92%, color-mix(in srgb, var(--color-coral) 20%, transparent), transparent 72%)',
            'radial-gradient(55% 55% at 8% 90%, color-mix(in srgb, var(--color-citron) 22%, transparent), transparent 72%)',
          ].join(', '),
        }}
      />
      {shapes.map((cfg, i) => (
        <Shape key={i} cfg={cfg} px={sx} py={sy} reduce={reduce} />
      ))}
    </div>
  );
}
