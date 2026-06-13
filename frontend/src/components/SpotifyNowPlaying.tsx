'use client';

import { useGetSpotifyNowPlaying } from '@/lib/api/generated';

export function SpotifyNowPlaying() {
  const { data } = useGetSpotifyNowPlaying({
    query: { refetchInterval: 30_000 },
  });

  const playing = data?.is_playing;

  return (
    <div className="mx-auto w-full max-w-4xl px-6">
      <a
        href="https://open.spotify.com/user/alifyandraid"
        target="_blank"
        rel="noopener noreferrer"
        className="flex items-center gap-3 rounded-lg border border-slate-700 bg-white/[0.02] px-4 py-3 no-underline transition hover:border-mint"
      >
        <span
          className={`h-2.5 w-2.5 shrink-0 rounded-full ${
            playing ? 'animate-pulse bg-mint' : 'bg-slate-600'
          }`}
        />
        <span className="text-sm text-slate-300">
          {playing ? (
            <>
              <span className="text-mint">Now playing</span> —{' '}
              <span className="font-medium text-white">{data?.title}</span>
              {data?.artists?.length ? (
                <span className="text-slate-400">
                  {' '}
                  · {data.artists.join(', ')}
                </span>
              ) : null}
            </>
          ) : (
            <span className="text-slate-400">
              Not playing right now — see my Spotify
            </span>
          )}
        </span>
      </a>
    </div>
  );
}
