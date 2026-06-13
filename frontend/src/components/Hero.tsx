import { profile } from '@/lib/resume';

export function Hero() {
  return (
    <header className="mx-auto w-full max-w-4xl px-6 pb-8 pt-24">
      <div className="flex flex-col gap-10 md:flex-row md:items-center md:justify-between md:gap-12">
        <div className="flex flex-col gap-6">
          <p className="font-mono text-sm text-mint">Hi, my name is</p>
          <h1 className="text-5xl font-extrabold tracking-tight text-white sm:text-6xl">
            {profile.nickname}
          </h1>
          <h2 className="text-2xl font-semibold text-sky sm:text-3xl">
            {profile.title}
          </h2>
          <p className="max-w-2xl whitespace-pre-line leading-relaxed text-slate-300">
            {profile.summary}
          </p>
          <div className="mt-2 flex flex-wrap gap-3">
            <a
              href="#contact"
              className="rounded-md bg-citron px-5 py-2.5 font-semibold text-deepsea no-underline transition hover:brightness-95"
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
            rest of the site (no next/image remote config needed). */}
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img
          src="/alif.jpg"
          alt={profile.name}
          className="mx-auto w-44 shrink-0 rounded-2xl border border-slate-700 object-cover shadow-xl shadow-black/40 sm:w-52 md:w-60"
        />
      </div>
    </header>
  );
}
