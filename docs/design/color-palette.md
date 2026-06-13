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
each ramp on the base hex above (the `500` step). The Tailwind config in
`frontend/tailwind.config.ts` is the source of truth for the generated ramps.

## Tailwind tokens

These are wired into `frontend/tailwind.config.ts`:

```ts
colors: {
  deepsea: '#053c5e', // background / primary text on light
  mint:    '#45cb85', // secondary / success
  sky:     '#96c5f7', // tertiary / links
  citron:  '#eaf27c', // primary accent / CTA
  coral:   '#f24333', // danger / error
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

```css
:root {
  --color-deepsea: #053c5e;
  --color-mint:    #45cb85;
  --color-sky:     #96c5f7;
  --color-citron:  #eaf27c;
  --color-coral:   #f24333;
}
```
