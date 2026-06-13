import { Section } from '@/components/Section';
import { photos } from '@/lib/photos';

export function PhotographyPanel() {
  return (
    <Section id="photography" title="Photography">
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
              className="group relative break-inside-avoid overflow-hidden rounded-lg"
            >
              {/* Local curated assets — plain img keeps the static panel simple. */}
              {/* eslint-disable-next-line @next/next/no-img-element */}
              <img
                src={photo.src}
                alt={photo.alt}
                className="w-full transition duration-300 group-hover:opacity-90"
              />
              {photo.caption && (
                <figcaption className="absolute inset-x-0 bottom-0 translate-y-full bg-gradient-to-t from-black/80 to-transparent px-3 py-2 text-xs text-slate-200 transition group-hover:translate-y-0">
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
