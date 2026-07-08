'use client';

import { Section } from './Section';
import { useListProjects } from '@/lib/api/generated';
import type { ProjectDTO } from '@/lib/api/model';

// Each card takes one of the four palette accents in rotation so the grid reads
// colourful. Tints (border/background/glow) use the raw palette vars via
// color-mix — fine in both themes because they render as backgrounds, not text.
// Readable text (tags, links) uses the literal text-* utilities, which carry the
// light-theme overrides in globals.css.
const ACCENTS = [
  { text: 'text-sky', v: 'var(--color-sky)' },
  { text: 'text-citron', v: 'var(--color-citron)' },
  { text: 'text-mint', v: 'var(--color-mint)' },
  { text: 'text-coral', v: 'var(--color-coral)' },
] as const;

export function Projects() {
  const { data, isLoading, isError } = useListProjects();

  return (
    <Section id="projects" title="Projects" eyebrow="things i've built" accent="sky">
      {isLoading && <p className="text-slate-400">Loading projects…</p>}
      {isError && (
        <p className="text-coral">
          Couldn&apos;t load projects. Is the API running?
        </p>
      )}
      <div className="grid grid-cols-1 gap-5 sm:grid-cols-2">
        {data?.projects?.map((p: ProjectDTO, i: number) => {
          const accent = ACCENTS[i % ACCENTS.length];
          return (
            <article
              key={p.slug}
              className="group relative flex h-full flex-col overflow-hidden rounded-2xl border p-5 transition duration-300 hover:-translate-y-1"
              style={{
                borderColor: `color-mix(in srgb, ${accent.v} 40%, transparent)`,
                background: `color-mix(in srgb, ${accent.v} 7%, var(--color-deepsea))`,
              }}
            >
              {/* soft glow on hover — no heavy drop-shadow */}
              <span
                aria-hidden
                className="pointer-events-none absolute -inset-px rounded-2xl opacity-0 transition-opacity duration-300 group-hover:opacity-100"
                style={{ boxShadow: `0 0 40px -12px ${accent.v}` }}
              />
              <h3 className="font-display text-lg font-bold text-white">
                {p.title}
              </h3>
              <p className="mt-1 text-sm leading-relaxed text-slate-300">
                {p.summary}
              </p>
              <div className="mt-3 flex flex-wrap gap-1.5">
                {p.tags?.map((t) => (
                  <span
                    key={t}
                    className={`rounded-full px-2.5 py-0.5 text-xs font-medium ${accent.text}`}
                    style={{
                      background: `color-mix(in srgb, ${accent.v} 15%, transparent)`,
                    }}
                  >
                    {t}
                  </span>
                ))}
              </div>
              <div className="mt-auto flex gap-4 pt-4 text-sm">
                {p.repo_url && (
                  <a
                    href={p.repo_url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className={`font-semibold no-underline hover:underline ${accent.text}`}
                  >
                    Code ↗
                  </a>
                )}
                {p.live_url && (
                  <a
                    href={p.live_url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className={`font-semibold no-underline hover:underline ${accent.text}`}
                  >
                    Live ↗
                  </a>
                )}
              </div>
            </article>
          );
        })}
      </div>
    </Section>
  );
}
