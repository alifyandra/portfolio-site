import type { Config } from 'tailwindcss';

// Palette source of truth: see /docs/design/color-palette.md
const config: Config = {
  content: ['./src/**/*.{ts,tsx,mdx}'],
  theme: {
    extend: {
      colors: {
        deepsea: '#053c5e', // background / primary text on light
        mint: '#45cb85', // secondary / success
        sky: '#96c5f7', // tertiary / links
        citron: '#eaf27c', // primary accent / CTA
        coral: '#f24333', // danger / error
      },
      fontFamily: {
        sans: ['var(--font-sans)', 'system-ui', 'sans-serif'],
      },
    },
  },
  plugins: [],
};

export default config;
