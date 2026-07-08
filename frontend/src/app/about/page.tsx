import type { Metadata } from 'next';
import { Hero } from '@/components/Hero';
import { Skills } from '@/components/Skills';
import { Experience } from '@/components/Experience';
import { Projects } from '@/components/Projects';
import { Contact } from '@/components/Contact';
import { aboutPanels } from '@/components/panels';
import { Footer } from '@/components/Footer';

export const metadata: Metadata = {
  title: 'About Alif · aliflabs',
  description:
    'The long version: Alif, a full-stack engineer in Melbourne. Projects, skills, experience, and a few personal panels behind aliflabs.',
};

// The portfolio, moved here from the root. Server component rendering the
// existing client sections unchanged.
export default function AboutPage() {
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
      <Footer />
    </main>
  );
}
