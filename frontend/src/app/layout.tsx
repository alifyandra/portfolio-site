import type { Metadata } from 'next';
import { Inter, Space_Grotesk, JetBrains_Mono } from 'next/font/google';
import './globals.css';
import { Providers } from './providers';
import { Navbar } from '@/components/Navbar';
import { Welcome } from '@/components/Welcome';

// Apply the theme to <html> before first paint so there's no flash: stored
// choice wins, else the OS preference, else dark. Mirrors ThemeToggle's writes.
const themeNoFlash = `(function(){try{var t=localStorage.getItem('theme');if(t!=='dark'&&t!=='light'){t=window.matchMedia('(prefers-color-scheme: light)').matches?'light':'dark';}document.documentElement.setAttribute('data-theme',t);}catch(e){document.documentElement.setAttribute('data-theme','dark');}})();`;

// Inter (body) → --font-inter → --font-sans. Space Grotesk (display wordmark +
// headings) → --font-display. JetBrains Mono (eyebrows/labels) → --font-mono.
// All three vars are wired into the @theme block in globals.css.
const inter = Inter({ subsets: ['latin'], variable: '--font-inter' });
const spaceGrotesk = Space_Grotesk({
  subsets: ['latin'],
  variable: '--font-space-grotesk',
});
const jetbrainsMono = JetBrains_Mono({
  subsets: ['latin'],
  variable: '--font-jetbrains-mono',
});

export const metadata: Metadata = {
  title: 'aliflabs — a workshop of small web tools',
  description:
    'aliflabs is a workshop of small, sharp web tools by Alif, a full-stack engineer in Melbourne. A few are live, more are on the bench.',
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html
      lang="en"
      className={`${inter.variable} ${spaceGrotesk.variable} ${jetbrainsMono.variable}`}
      suppressHydrationWarning
    >
      <head>
        <script dangerouslySetInnerHTML={{ __html: themeNoFlash }} />
      </head>
      <body>
        <Providers>
          <Navbar />
          <Welcome />
          {children}
        </Providers>
      </body>
    </html>
  );
}
