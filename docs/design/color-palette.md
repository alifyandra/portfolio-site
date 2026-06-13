# Colour Palette

The portfolio's visual identity. Five colours, each assigned a single semantic
role so usage stays consistent across the Next.js app and any future surfaces.

## The colours

| Swatch | Hex | Name | Semantic role |
|--------|-----|------|---------------|
| 🟦 | `#053c5e` | **Deep Sea** | Primary background / dark base, headings on light |
| 🟩 | `#45cb85` | **Mint** | Secondary / success / positive accents |
| 🟦 | `#96c5f7` | **Sky** | Tertiary / links / informational accents |
| 🟨 | `#eaf27c` | **Citron** | Primary accent / CTAs / highlights |
| 🟥 | `#f24333` | **Coral** | Danger / errors / attention pops |

## Roles in detail

- **Deep Sea `#053c5e`** — the anchor. Used as the dark page background and for
  primary text on light surfaces. Everything else reads as an accent against it.
- **Citron `#eaf27c`** — the signature pop. Reserve for the most important
  single action on a view (primary CTA, active nav, key highlight). Overuse
  kills its impact. High-energy against Deep Sea.
- **Mint `#45cb85`** — secondary actions, success states, the Spotify "now
  playing" pulse, positive tags.
- **Sky `#96c5f7`** — hyperlinks, info banners, subtle interactive hovers,
  secondary tags.
- **Coral `#f24333`** — destructive actions, form errors, validation. Used
  sparingly and only to mean "stop / wrong / careful".

## Contrast & accessibility notes

- `Citron`, `Mint`, and `Sky` are all **light** colours — use **Deep Sea text
  on them**, never white. (Citron-on-white and Sky-on-white fail WCAG AA.)
- `Deep Sea` is dark — use **white or Citron text on it** (both pass AA).
- `Coral` on `Deep Sea` passes AA for large text; for body-size error text,
  pair Coral with a lighter tint or use it on a light background.
- Always verify final pairings at https://webaim.org/resources/contrastchecker/.

## Tints / shades (suggested steps)

For hover/disabled/border states, generate 50–900 ramps in Tailwind. Anchor
each ramp on the base hex above (the `500` step). The `@theme` block in
`frontend/src/app/globals.css` is the source of truth for the palette.

## Tailwind tokens

Tailwind v4 is CSS-first — the palette is declared in the `@theme` block of
`frontend/src/app/globals.css` (there is no `tailwind.config.ts`). Each
`--color-NAME` token auto-generates the `bg-NAME`, `text-NAME` and
`border-NAME` utilities used across the app:

```css
/* frontend/src/app/globals.css */
@theme {
  --color-deepsea: #053c5e; /* background / primary text on light */
  --color-mint:    #45cb85; /* secondary / success */
  --color-sky:     #96c5f7; /* tertiary / links */
  --color-citron:  #eaf27c; /* primary accent / CTA */
  --color-coral:   #f24333; /* danger / error */
}
```

Usage examples:

```tsx
<button className="bg-citron text-deepsea hover:brightness-95">Get in touch</button>
<a className="text-sky hover:underline">View source</a>
<span className="bg-mint/20 text-deepsea rounded px-2">Open to work</span>
<p className="text-coral">This field is required.</p>
```

## CSS variables (for non-Tailwind contexts)

The `@theme` tokens above are themselves exposed as `--color-*` custom
properties on `:root`, so non-Tailwind CSS can reference them directly — no
separate `:root` block needed:

```css
background: var(--color-citron);
color: var(--color-deepsea);
```

## Tailwind v4 gotcha: base styles must live in `@layer base`

Tailwind v4 emits real CSS cascade layers (`theme, base, components,
utilities`). **Unlayered CSS beats every layer**, so any custom base rule
written outside a layer will override utility classes regardless of
specificity. In `globals.css` an unlayered `a { @apply text-sky }` once
overrode `text-deepsea` / `text-mint` / `text-coral` on every link and
link-button (e.g. the citron "Get in touch" CTA rendered with bright sky
text). v3 didn't have this problem because it flattened layers.

Rule of thumb: **wrap element-level base styles (`body`, `a`, `::selection`,
placeholder/cursor resets, …) in `@layer base`** so utilities can still win:

```css
@layer base {
  a { @apply text-sky; }
  /* … */
}
```

Per-element utility classes (`text-deepsea`, `bg-citron`, …) live in the
`utilities` layer and will then correctly override these defaults.
