import { profile } from '@/lib/resume';

export function Hero() {
  return (
    <header className="mx-auto flex w-full max-w-4xl flex-col gap-6 px-6 pb-8 pt-24">
      <p className="font-mono text-sm text-mint">Hi, my name is</p>
      <h1 className="text-5xl font-extrabold tracking-tight text-white sm:text-6xl">
        {profile.name}{' '}
        <span className="text-slate-400">({profile.nickname})</span>
      </h1>
      <h2 className="text-2xl font-semibold text-sky sm:text-3xl">
        {profile.title}
      </h2>
      <p className="max-w-2xl leading-relaxed text-slate-300">
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
    </header>
  );
}
