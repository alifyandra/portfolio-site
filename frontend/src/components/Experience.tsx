import { Section } from './Section';
import { experience, education } from '@/lib/resume';

export function Experience() {
  return (
    <Section id="experience" title="Work Experience">
      <ol className="relative border-l border-slate-700">
        {experience.map((job) => (
          <li key={`${job.company}-${job.period}`} className="mb-10 ml-6">
            <span className="absolute -left-1.5 mt-1.5 h-3 w-3 rounded-full bg-mint" />
            <h3 className="text-lg font-semibold text-white">
              {job.role} ·{' '}
              <span className="text-sky">{job.company}</span>
            </h3>
            <p className="mb-2 font-mono text-xs text-slate-400">
              {job.period}
            </p>
            <ul className="list-disc space-y-1 pl-5 text-slate-300">
              {job.points.map((p, i) => (
                <li key={i}>{p}</li>
              ))}
            </ul>
          </li>
        ))}
      </ol>

      <h3 className="section-title mb-3 mt-6 text-lg font-semibold">
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
