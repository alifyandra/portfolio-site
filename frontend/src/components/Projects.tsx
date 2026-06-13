'use client';

import { Section } from './Section';
import { useListProjects } from '@/lib/api/generated';
import type { ProjectDTO } from '@/lib/api/model';

export function Projects() {
  const { data, isLoading, isError } = useListProjects();

  return (
    <Section id="projects" title="Projects">
      {isLoading && <p className="text-slate-400">Loading projects…</p>}
      {isError && (
        <p className="text-coral">
          Couldn&apos;t load projects — is the API running?
        </p>
      )}
      <div className="grid grid-cols-1 gap-5 sm:grid-cols-2">
        {data?.projects?.map((p: ProjectDTO) => (
          <article
            key={p.slug}
            className="rounded-lg border border-slate-700 bg-white/[0.02] p-5 transition hover:border-sky"
          >
            <h3 className="text-lg font-semibold text-white">{p.title}</h3>
            <p className="mt-1 text-sm text-slate-300">{p.summary}</p>
            <div className="mt-3 flex flex-wrap gap-1.5">
              {p.tags?.map((t) => (
                <span
                  key={t}
                  className="rounded bg-mint/15 px-2 py-0.5 text-xs text-mint"
                >
                  {t}
                </span>
              ))}
            </div>
            <div className="mt-4 flex gap-4 text-sm">
              {p.repo_url && (
                <a href={p.repo_url} target="_blank" rel="noopener noreferrer">
                  Code ↗
                </a>
              )}
              {p.live_url && (
                <a href={p.live_url} target="_blank" rel="noopener noreferrer">
                  Live ↗
                </a>
              )}
            </div>
          </article>
        ))}
      </div>
    </Section>
  );
}
