// Static-but-shaped photo data for the Photography About Panel.
//
// Stage 1: curated images live in the frontend (drop files in /public/photos
// and list them here). The shape deliberately mirrors what a future API
// response would return, so swapping to a dynamic source (S3 + Ent + admin
// upload, gated on auth) is a data-layer change — the panel JSX stays put.

export type Photo = {
  /** Path under /public, e.g. "/photos/sunset.jpg". */
  src: string;
  /** Accessibility text. */
  alt: string;
  /** Optional caption shown on hover / under the image. */
  caption?: string;
};

export const photos: Photo[] = [
  { src: '/photos/photo-01.jpg', alt: 'Photograph by Alif' },
  { src: '/photos/photo-02.jpg', alt: 'Photograph by Alif' },
  { src: '/photos/photo-03.jpg', alt: 'Photograph by Alif' },
  { src: '/photos/photo-04.jpg', alt: 'Photograph by Alif' },
  { src: '/photos/photo-05.jpg', alt: 'Photograph by Alif' },
  { src: '/photos/photo-06.jpg', alt: 'Photograph by Alif' },
  { src: '/photos/photo-07.jpg', alt: 'Photograph by Alif' },
  { src: '/photos/photo-08.jpg', alt: 'Photograph by Alif' },
  { src: '/photos/photo-09.jpg', alt: 'Photograph by Alif' },
];
