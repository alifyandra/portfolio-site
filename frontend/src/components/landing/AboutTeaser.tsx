import Link from 'next/link';
import { profile } from '@/lib/resume';

// Quiet strip linking to the full portfolio at /about. Static server component.
export function AboutTeaser() {
  return (
    <section className="mx-auto w-full max-w-4xl px-6 pb-8">
      <div className="flex flex-col gap-5 rounded-2xl border border-slate-800 bg-white/[0.02] p-6 sm:flex-row sm:items-center sm:gap-6">
        {/* Plain img: local asset, no Next/Image config needed. */}
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img
          src="/alif.jpg"
          alt={profile.name}
          className="h-16 w-16 shrink-0 rounded-full border border-slate-800 object-cover"
        />
        <div className="flex flex-col gap-1">
          <p className="font-mono text-xs lowercase tracking-widest text-mint">
            behind aliflabs
          </p>
          <h2 className="font-display text-xl font-bold text-white">
            Behind aliflabs
          </h2>
          <p className="text-sm leading-relaxed text-slate-400">
            Alif builds these. A full-stack engineer in Melbourne, shipping
            small things here between the bigger ones.
          </p>
          <Link
            href="/about"
            className="mt-1 self-start text-sm font-semibold text-sky no-underline hover:underline"
          >
            Read the long version →
          </Link>
        </div>
      </div>
    </section>
  );
}
