import { Section } from './Section';
import { skills } from '@/lib/resume';

export function Skills() {
  return (
    <Section id="skills" title="Skills">
      <div className="grid grid-cols-1 gap-6 sm:grid-cols-2">
        {skills.map((s) => (
          <div key={s.group}>
            <h3 className="mb-2 font-mono text-sm text-mint">{s.group}</h3>
            <div className="flex flex-wrap gap-2">
              {s.items.map((item) => (
                <span
                  key={item}
                  className="rounded-sm bg-sky/10 px-2.5 py-1 text-sm text-sky"
                >
                  {item}
                </span>
              ))}
            </div>
          </div>
        ))}
      </div>
    </Section>
  );
}
