// Simple geometric glyphs for the tool cards — one distinct shape per card,
// tinted to the card's accent via currentColor. No emoji, no imagery, so
// nothing to worry about with shadows/rings (house preference).

type GlyphProps = { className?: string };

const base = {
  width: 44,
  height: 44,
  viewBox: '0 0 44 44',
  fill: 'none' as const,
  stroke: 'currentColor',
  strokeWidth: 2.5,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
  'aria-hidden': true,
};

// A message-ish squiggle wave.
function Squiggle({ className }: GlyphProps) {
  return (
    <svg {...base} className={className}>
      <path d="M4 26c5-10 9-10 14 0s9 10 14 0 8-9 8-9" />
      <circle cx="6" cy="34" r="1.6" fill="currentColor" stroke="none" />
    </svg>
  );
}

// An orbit: a plus inside a ring.
function Orbit({ className }: GlyphProps) {
  return (
    <svg {...base} className={className}>
      <circle cx="22" cy="22" r="15" />
      <path d="M22 15v14M15 22h14" />
    </svg>
  );
}

// A triangle with a soft arc under it.
function Prism({ className }: GlyphProps) {
  return (
    <svg {...base} className={className}>
      <path d="M22 8 37 34H7z" />
      <path d="M13 40c3-3 15-3 18 0" />
    </svg>
  );
}

// A stack of arcs (a wave-form / bracket motif).
function Arcs({ className }: GlyphProps) {
  return (
    <svg {...base} className={className}>
      <path d="M12 8a20 20 0 0 1 0 28" />
      <path d="M22 12a14 14 0 0 1 0 20" />
      <path d="M32 16a8 8 0 0 1 0 12" />
    </svg>
  );
}

const GLYPHS = [Squiggle, Orbit, Prism, Arcs];

// Deterministic pick so each card in the grid gets a distinct shape.
export function Glyph({ index, className }: { index: number; className?: string }) {
  const Shape = GLYPHS[index % GLYPHS.length];
  return <Shape className={className} />;
}
