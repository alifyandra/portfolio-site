import { Hero } from '@/components/Hero';
import { Skills } from '@/components/Skills';
import { Experience } from '@/components/Experience';
import { Projects } from '@/components/Projects';
import { Contact } from '@/components/Contact';
import { aboutPanels } from '@/components/panels';
import { profile } from '@/lib/resume';

export default function Home() {
  return (
    <main className="min-h-screen">
      <Hero />
      <Projects />
      <Skills />
      <Experience />
      {aboutPanels.map((Panel, i) => (
        <Panel key={i} />
      ))}
      <Contact />
      <footer className="mx-auto w-full max-w-4xl px-6 py-12 text-sm text-slate-500">
        <p>
          Built with Go, Next.js & Tailwind ·{' '}
          <a href={profile.github} target="_blank" rel="noopener noreferrer">
            source
          </a>
        </p>
        <p className="mt-1">© {new Date().getFullYear()} {profile.name}</p>
      </footer>
    </main>
  );
}
