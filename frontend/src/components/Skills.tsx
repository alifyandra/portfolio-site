import { Section } from './Section';
import { skills } from '@/lib/resume';

// Each skill category owns one accent so the section reads colourful, not flat.
// The accent class sets --accent, consumed by .skill-group / .skill-chip in
// globals.css, which decide tinted (dark) vs solid (cool-light) per palette.
const ACCENT_CLASSES = [
  'accent-sky',
  'accent-citron',
  'accent-mint',
  'accent-coral',
];

export function Skills() {
  return (
    <Section id="skills" title="Skills" eyebrow="the toolkit" accent="citron">
      <div className="grid grid-cols-1 gap-6 sm:grid-cols-2">
        {skills.map((s, i) => (
          <div key={s.group} className={ACCENT_CLASSES[i % ACCENT_CLASSES.length]}>
            <h3 className="skill-group mb-2 font-mono text-sm">{s.group}</h3>
            <div className="flex flex-wrap gap-2">
              {s.items.map((item) => (
                <span key={item} className="skill-chip rounded-full px-3 py-1 text-sm">
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
