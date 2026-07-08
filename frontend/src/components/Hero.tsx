import { profile } from '@/lib/resume';

export function Hero() {
  return (
    <header className="relative overflow-hidden">
      {/* Ambient palette wash + a couple of static accent shapes. Purely
          decorative and CSS-only (Hero stays a server component); the shared
          .hero-ambient class softens it on the light canvas. Kept low-opacity
          so the dense content below stays readable, and the crisp shapes are
          desktop-only to keep 360px mobile uncluttered. */}
      <div
        aria-hidden
        className="hero-ambient pointer-events-none absolute inset-0 -z-10 overflow-hidden"
      >
        <div
          className="absolute inset-0"
          style={{
            background: [
              'radial-gradient(50% 60% at 10% 6%, color-mix(in srgb, var(--color-mint) 20%, transparent), transparent 70%)',
              'radial-gradient(46% 58% at 94% 16%, color-mix(in srgb, var(--color-sky) 18%, transparent), transparent 70%)',
              'radial-gradient(52% 52% at 86% 96%, color-mix(in srgb, var(--color-citron) 16%, transparent), transparent 72%)',
            ].join(', '),
          }}
        />
        {/* sky ring, up near the portrait */}
        <svg
          className="absolute right-[8%] top-[20%] hidden text-sky/45 sm:block"
          width="58"
          height="58"
          viewBox="0 0 100 100"
          aria-hidden
        >
          <circle
            cx="50"
            cy="50"
            r="42"
            fill="none"
            stroke="currentColor"
            strokeWidth="7"
          />
        </svg>
        {/* citron sparkle, near the wordmark */}
        <svg
          className="absolute left-[34%] top-[16%] hidden text-citron/55 md:block"
          width="34"
          height="34"
          viewBox="0 0 100 100"
          aria-hidden
        >
          <path
            fill="currentColor"
            d="M50 6c4 30 14 40 44 44-30 4-40 14-44 44-4-30-14-40-44-44 30-4 40-14 44-44z"
          />
        </svg>
      </div>

      <div className="mx-auto w-full max-w-4xl px-6 pb-8 pt-24">
        <div className="flex flex-col gap-10 md:flex-row md:items-center md:justify-between md:gap-12">
          <div className="flex flex-col gap-6">
            <p className="font-mono text-sm lowercase tracking-wide text-mint">
              hi, my name is
            </p>
            <h1 className="font-display text-6xl font-bold tracking-tight text-white sm:text-7xl">
              {profile.nickname}
            </h1>
            <h2 className="font-display text-2xl font-semibold text-sky sm:text-3xl">
              {profile.title}
            </h2>
            <p className="max-w-2xl whitespace-pre-line leading-relaxed text-slate-300">
              {profile.summary}
            </p>
            <div className="mt-2 flex flex-wrap gap-3">
              <a
                href="#contact"
                className="rounded-md bg-citron px-5 py-2.5 font-semibold text-ink no-underline transition hover:brightness-95"
              >
                Get in touch
              </a>
              <a
                href={profile.linkedin}
                target="_blank"
                rel="noopener noreferrer"
                className="rounded-md px-5 py-2.5 font-semibold text-slate-300 no-underline transition hover:text-white"
              >
                LinkedIn
              </a>
              <a
                href={profile.github}
                target="_blank"
                rel="noopener noreferrer"
                className="rounded-md px-5 py-2.5 font-semibold text-slate-300 no-underline transition hover:text-white"
              >
                GitHub
              </a>
            </div>
          </div>

          {/* Local portrait in public/. Plain img keeps it consistent with the
              rest of the site (no next/image remote config needed). Soft border
              only — no heavy ring/shadow on imagery (house preference). */}
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img
            src="/alif.jpg"
            alt={profile.name}
            className="mx-auto w-44 shrink-0 rounded-2xl border border-slate-700 object-cover sm:w-52 md:w-60"
          />
        </div>
      </div>
    </header>
  );
}
