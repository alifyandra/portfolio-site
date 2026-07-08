import { LandingHero } from '@/components/landing/LandingHero';
import { ToolsBench } from '@/components/landing/ToolsBench';
import { AboutTeaser } from '@/components/landing/AboutTeaser';
import { Footer } from '@/components/Footer';

// aliflabs landing. Server component composing the (client) hero + bench and
// the static about teaser. Page title/description come from the root layout.
export default function Home() {
  return (
    <main className="min-h-screen">
      <LandingHero />
      <ToolsBench />
      <AboutTeaser />
      <Footer />
    </main>
  );
}
