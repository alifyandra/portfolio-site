// The two-colour aliflabs wordmark, defined once and reused by the navbar
// (small) and the landing hero (huge, via a size className). "alif" in citron
// and "labs" in sky: both are palette accents that stay legible in dark and
// light (text-citron / text-sky carry light-theme overrides in globals.css).
export function BrandMark({ className }: { className?: string }) {
  return (
    <span
      className={`font-display font-bold tracking-tight ${className ?? ''}`}
    >
      <span className="text-citron">alif</span>
      <span className="text-sky">labs</span>
    </span>
  );
}
