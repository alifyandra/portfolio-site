import { Section } from '@/components/Section';
import { photos } from '@/lib/photos';

export function PhotographyPanel() {
  return (
    <Section
      id="photography"
      title="Photos I've Taken"
      eyebrow="through my lens"
      accent="sky"
    >
      {photos.length === 0 ? (
        <p className="text-sm text-slate-500">
          A few shots, coming soon.
        </p>
      ) : (
        // Masonry via CSS columns — images keep their natural aspect ratio.
        <div className="columns-2 gap-3 sm:columns-3 [&>figure]:mb-3">
          {photos.map((photo) => (
            <figure
              key={photo.src}
              // soft tinted border on hover only — no heavy ring/shadow on imagery
              className="group relative break-inside-avoid overflow-hidden rounded-lg border border-transparent transition-colors duration-300 hover:border-sky/40"
            >
              {/* Local curated assets — plain img keeps the static panel simple. */}
              {/* eslint-disable-next-line @next/next/no-img-element */}
              <img
                src={photo.src}
                alt={photo.alt}
                className="w-full transition duration-300 group-hover:opacity-90"
              />
              {photo.caption && (
                <figcaption className="absolute inset-x-0 bottom-0 translate-y-full bg-linear-to-t from-black/80 to-transparent px-3 py-2 text-xs text-slate-200 transition group-hover:translate-y-0">
                  {photo.caption}
                </figcaption>
              )}
            </figure>
          ))}
        </div>
      )}
    </Section>
  );
}
