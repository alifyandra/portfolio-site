'use client';

/**
 * Dark/light theme toggle. Lives inline in the global Navbar. Writes
 * html[data-theme] (read by globals.css) and persists the choice in
 * localStorage. A matching no-flash inline script in layout.tsx applies the
 * stored/system theme before first paint. Styled with theme tokens so it looks
 * native in either theme.
 */
import { useSyncExternalStore } from 'react';

type Theme = 'dark' | 'light';

// The current theme lives outside React, on <html data-theme> (written by the
// no-flash script before hydration, then by this toggle). We read it through
// useSyncExternalStore — the React-blessed way to subscribe to browser state —
// so there's no setState-in-effect and no hydration mismatch: SSR + first
// client render use the server snapshot ('dark'), then React reconciles to the
// value the no-flash script actually applied.
const listeners = new Set<() => void>();

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

function getSnapshot(): Theme {
  return document.documentElement.getAttribute('data-theme') === 'light'
    ? 'light'
    : 'dark';
}

function getServerSnapshot(): Theme {
  return 'dark';
}

function applyTheme(t: Theme) {
  document.documentElement.setAttribute('data-theme', t);
  try {
    localStorage.setItem('theme', t);
  } catch {
    /* ignore storage failures (private mode, etc.) */
  }
  listeners.forEach((l) => l());
}

const SunIcon = () => (
  <svg
    width="18"
    height="18"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
    aria-hidden="true"
  >
    <circle cx="12" cy="12" r="4" />
    <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41" />
  </svg>
);

const MoonIcon = () => (
  <svg
    width="18"
    height="18"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
    aria-hidden="true"
  >
    <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
  </svg>
);

export function ThemeToggle() {
  const theme = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
  const next: Theme = theme === 'dark' ? 'light' : 'dark';

  return (
    <button
      type="button"
      onClick={() => applyTheme(next)}
      aria-label={`Switch to ${next} theme`}
      title={`Switch to ${next} theme`}
      className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full border border-slate-700 bg-deepsea text-slate-200 transition hover:text-citron"
    >
      {theme === 'dark' ? <SunIcon /> : <MoonIcon />}
    </button>
  );
}
