import type { Metadata } from 'next';
import { Inter } from 'next/font/google';
import './globals.css';
import { Providers } from './providers';

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
    <html lang="en" className={inter.variable}>
      <body>
        <Providers>{children}</Providers>
      </body>
    </html>
  );
}
