import { Section } from './Section';
import { experience, education } from '@/lib/resume';

// Each timeline entry rotates through the palette (dot + company name) so the
// column reads lively rather than flat. Colours use literal bg-*/text-*
// utilities so both themes flip automatically.
const ACCENTS = [
  { dot: 'bg-mint', text: 'text-mint' },
  { dot: 'bg-sky', text: 'text-sky' },
  { dot: 'bg-citron', text: 'text-citron' },
  { dot: 'bg-coral', text: 'text-coral' },
] as const;

export function Experience() {
  return (
    <Section
      id="experience"
      title="Work Experience"
      eyebrow="the road so far"
      accent="mint"
    >
      <ol className="relative border-l border-slate-700">
        {experience.map((job, i) => {
          const accent = ACCENTS[i % ACCENTS.length];
          return (
            <li key={`${job.company}-${job.period}`} className="mb-10 ml-6">
              <span
                className={`absolute -left-[7px] mt-1.5 h-3.5 w-3.5 rounded-full border-2 border-deepsea ${accent.dot}`}
              />
              <h3 className="font-display text-lg font-bold text-white">
                {job.role} ·{' '}
                <span className={accent.text}>{job.company}</span>
              </h3>
              <p className="mb-2 font-mono text-xs text-slate-400">
                {job.period}
              </p>
              <ul className="list-disc space-y-1 pl-5 text-slate-300">
                {job.points.map((p, idx) => (
                  <li key={idx}>{p}</li>
                ))}
              </ul>
            </li>
          );
        })}
      </ol>

      <h3 className="section-title mb-3 mt-6 font-display text-lg font-bold">
        Education & Certs
      </h3>
      <ul className="space-y-2 text-slate-300">
        {education.map((e) => (
          <li key={e.title}>
            <span className="font-medium text-white">{e.title}</span>,{' '}
            {e.org}{' '}
            <span className="text-slate-400">({e.year})</span>
          </li>
        ))}
      </ul>
    </Section>
  );
}
