import { profile } from '@/lib/resume';

// Shared footer, used by the landing (/) and the about page. Text unchanged
// from the original portfolio footer.
export function Footer() {
  return (
    <footer className="mx-auto w-full max-w-4xl px-6 py-12 text-sm text-slate-500">
      <p>
        Built with Go, Next.js & Tailwind ·{' '}
        <a href={profile.github} target="_blank" rel="noopener noreferrer">
          source
        </a>
      </p>
      <p className="mt-1">
        © {new Date().getFullYear()} {profile.name}
      </p>
    </footer>
  );
}
