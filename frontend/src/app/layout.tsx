import type { Metadata } from 'next';
import { Inter } from 'next/font/google';
import './globals.css';
import { Providers } from './providers';
import { ThemeToggle } from '@/components/ThemeToggle';

// Apply the theme to <html> before first paint so there's no flash: stored
// choice wins, else the OS preference, else dark. Mirrors ThemeToggle's writes.
const themeNoFlash = `(function(){try{var t=localStorage.getItem('theme');if(t!=='dark'&&t!=='light'){t=window.matchMedia('(prefers-color-scheme: light)').matches?'light':'dark';}document.documentElement.setAttribute('data-theme',t);}catch(e){document.documentElement.setAttribute('data-theme','dark');}})();`;

// Exposed as --font-inter and consumed by the Tailwind v4 --font-sans token
// (see globals.css) so it drives both the default body font and font-sans.
const inter = Inter({ subsets: ['latin'], variable: '--font-inter' });

export const metadata: Metadata = {
  title: 'Ahmad Alifyandra · Full-Stack Engineer',
  description:
    'Full-stack engineer in Melbourne. Python, Django, Next.js, Redis, AWS.',
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en" className={inter.variable} suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: themeNoFlash }} />
      </head>
      <body>
        <ThemeToggle />
        <Providers>{children}</Providers>
      </body>
    </html>
  );
}
