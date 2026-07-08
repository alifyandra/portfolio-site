import type { ReactNode } from 'react';

// Shared section shell for the About/portfolio page. Each section carries a
// lowercase mono eyebrow in a palette accent, a display-font title (kept on the
// existing .section-title heading colour so both themes flip), and a small
// coloured squiggle flourish — the same playful language as the landing.
//
// The accent only drives the *decorative* eyebrow + squiggle. Their colour uses
// the literal text-* utilities (not inline vars) so the light-theme overrides in
// globals.css (readable citron/mint) apply automatically.
export type SectionAccent = 'mint' | 'sky' | 'citron' | 'coral';

const ACCENT_TEXT: Record<SectionAccent, string> = {
  mint: 'text-mint',
  sky: 'text-sky',
  citron: 'text-citron',
  coral: 'text-coral',
};

export function Section({
  id,
  title,
  eyebrow,
  accent = 'mint',
  children,
}: {
  id: string;
  title: string;
  eyebrow?: string;
  accent?: SectionAccent;
  children: ReactNode;
}) {
  const accentText = ACCENT_TEXT[accent];
  return (
    <section id={id} className="mx-auto w-full max-w-4xl px-6 py-16">
      <div className="mb-8">
        {eyebrow && (
          <p
            className={`font-mono text-sm lowercase tracking-wide ${accentText}`}
          >
            {eyebrow}
          </p>
        )}
        <h2 className="section-title mt-2 font-display text-3xl font-bold sm:text-4xl">
          {title}
        </h2>
        {/* small coloured squiggle so the heading doesn't feel bare */}
        <svg
          width="104"
          height="10"
          viewBox="0 0 132 12"
          fill="none"
          aria-hidden
          className={`mt-3 ${accentText}`}
        >
          <path
            d="M2 7c8-6 16-6 24 0s16 6 24 0 16-6 24 0 16 6 24 0 16-6 24 0"
            stroke="currentColor"
            strokeWidth="3"
            strokeLinecap="round"
          />
        </svg>
      </div>
      {children}
    </section>
  );
}
