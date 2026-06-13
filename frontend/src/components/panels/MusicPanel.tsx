'use client';

import { Section } from '@/components/Section';
import {
  useGetSpotifyNowPlaying,
  useGetSpotifyPlaylists,
  useGetSpotifyTopArtists,
  useGetSpotifyTopTracks,
} from '@/lib/api/generated';
import type { TrackBody } from '@/lib/api/model';

function artistLine(artists?: string[] | null) {
  return artists?.length ? artists.join(', ') : '';
}

function TrackArt({ track, size }: { track: TrackBody; size: number }) {
  if (!track.album_image) {
    return (
      <div
        className="shrink-0 rounded bg-sky/10"
        style={{ width: size, height: size }}
      />
    );
  }
  return (
    // Remote Spotify CDN art — plain img avoids next/image remote config.
    // eslint-disable-next-line @next/next/no-img-element
    <img
      src={track.album_image}
      alt={track.album ?? ''}
      width={size}
      height={size}
      className="shrink-0 rounded object-cover"
    />
  );
}

function TrackLink({
  track,
  children,
  className,
}: {
  track: TrackBody;
  children: React.ReactNode;
  className?: string;
}) {
  if (!track.song_url) return <div className={className}>{children}</div>;
  return (
    <a
      href={track.song_url}
      target="_blank"
      rel="noopener noreferrer"
      className={className}
    >
      {children}
    </a>
  );
}

export function MusicPanel() {
  const { data: now } = useGetSpotifyNowPlaying({
    query: { refetchInterval: 30_000 },
  });
  const { data: top } = useGetSpotifyTopTracks();
  const { data: artistData } = useGetSpotifyTopArtists();
  const { data: playlistData } = useGetSpotifyPlaylists();

  const track = now?.track;
  const live = now?.source === 'now-playing';
  const topTracks = top?.tracks ?? [];
  const topArtists = artistData?.artists ?? [];
  const playlists = playlistData?.playlists ?? [];

  return (
    <Section id="music" title="Music">
      {/* Header reflects live state so it's clear when it's real-time. */}
      <p className="mb-3 flex items-center gap-2 font-mono text-sm">
        {live ? (
          <>
            <span className="h-2 w-2 animate-pulse rounded-full bg-mint" />
            <span className="text-mint">Currently listening to</span>
            <span className="rounded-full bg-mint/15 px-1.5 py-0.5 text-[10px] font-semibold tracking-wide text-mint">
              LIVE
            </span>
          </>
        ) : (
          <span className="text-slate-500">Last played</span>
        )}
      </p>

      {/* Now playing / last played */}
      {track ? (
        <TrackLink
          track={track}
          className="flex items-center gap-4 rounded-lg border border-slate-700 bg-white/[0.02] px-4 py-3 no-underline transition hover:border-mint"
        >
          <TrackArt track={track} size={56} />
          <div className="min-w-0">
            <div className="truncate font-medium text-white">{track.title}</div>
            <div className="truncate text-sm text-slate-400">
              {artistLine(track.artists)}
            </div>
            {track.album && (
              <div className="truncate text-xs text-slate-500">
                {track.album}
              </div>
            )}
          </div>
        </TrackLink>
      ) : (
        <a
          href="https://open.spotify.com/user/alifyandraid"
          target="_blank"
          rel="noopener noreferrer"
          className="flex items-center gap-3 rounded-lg border border-slate-700 bg-white/[0.02] px-4 py-3 text-sm text-slate-400 no-underline transition hover:border-mint"
        >
          <span className="h-2 w-2 shrink-0 rounded-full bg-slate-600" />
          Nothing playing right now — see my Spotify
        </a>
      )}

      {/* Top tracks */}
      {topTracks.length > 0 && (
        <div className="mt-8">
          <h3 className="mb-3 font-mono text-sm text-mint">
            On repeat lately
          </h3>
          <ol className="space-y-1">
            {topTracks.map((t, i) => (
              <li key={`${t.title}-${i}`}>
                <TrackLink
                  track={t}
                  className="flex items-center gap-3 rounded-md px-2 py-1.5 no-underline transition hover:bg-white/[0.03]"
                >
                  <span className="w-4 shrink-0 text-right font-mono text-xs text-slate-500">
                    {i + 1}
                  </span>
                  <TrackArt track={t} size={36} />
                  <div className="min-w-0">
                    <div className="truncate text-sm text-slate-200">
                      {t.title}
                    </div>
                    <div className="truncate text-xs text-slate-500">
                      {artistLine(t.artists)}
                    </div>
                  </div>
                </TrackLink>
              </li>
            ))}
          </ol>
        </div>
      )}

      {/* Top artists */}
      {topArtists.length > 0 && (
        <div className="mt-8">
          <h3 className="mb-3 font-mono text-sm text-mint">
            Artists on heavy rotation
          </h3>
          <div className="grid grid-cols-3 gap-3 sm:grid-cols-4">
            {topArtists.map((a) => (
              <a
                key={a.url ?? a.name}
                href={a.url}
                target="_blank"
                rel="noopener noreferrer"
                className="group text-center no-underline"
              >
                {a.image ? (
                  // Remote Spotify CDN art — plain img avoids next/image config.
                  // eslint-disable-next-line @next/next/no-img-element
                  <img
                    src={a.image}
                    alt={a.name}
                    className="mx-auto mb-2 aspect-square w-full rounded-full object-cover transition group-hover:opacity-90"
                  />
                ) : (
                  <div className="mx-auto mb-2 aspect-square w-full rounded-full bg-sky/10" />
                )}
                <div className="truncate text-xs text-slate-300 transition group-hover:text-mint">
                  {a.name}
                </div>
              </a>
            ))}
          </div>
        </div>
      )}

      {/* Favourite playlists */}
      {playlists.length > 0 && (
        <div className="mt-8">
          <h3 className="mb-3 font-mono text-sm text-mint">Playlists I love</h3>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
            {playlists.map((p) => (
              <a
                key={p.url ?? p.name}
                href={p.url}
                target="_blank"
                rel="noopener noreferrer"
                className="group rounded-lg border border-slate-700 bg-white/[0.02] p-3 no-underline transition hover:border-mint"
              >
                {p.image ? (
                  // Remote Spotify CDN art — plain img avoids next/image config.
                  // eslint-disable-next-line @next/next/no-img-element
                  <img
                    src={p.image}
                    alt={p.name}
                    className="mb-2 aspect-square w-full rounded object-cover transition group-hover:opacity-90"
                  />
                ) : (
                  <div className="mb-2 aspect-square w-full rounded bg-sky/10" />
                )}
                <div className="truncate text-sm font-medium text-slate-200">
                  {p.name}
                </div>
              </a>
            ))}
          </div>
        </div>
      )}
    </Section>
  );
}
