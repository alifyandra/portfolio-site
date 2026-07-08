'use client';

// Playlists section of the Admin Console (ADR 12): manage the curated Spotify
// set that the Music panel renders. The set moved from a hardcoded const to a
// DB table, so this is the source of truth. Writes go to /api/admin/playlists
// behind the server-enforced admin middleware.
//
// The admin list returns only { id, spotify_id, sort_order }. We hydrate
// name/artwork by matching against the public /api/spotify/playlists payload
// (which carries name + image + url) on the playlist id parsed out of its url;
// when there's no match (e.g. Spotify unreachable) we fall back to the raw id.

import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';

import {
  useListPlaylists,
  useCreatePlaylist,
  useDeletePlaylist,
  useReorderPlaylists,
  useGetSpotifyPlaylists,
  getListPlaylistsQueryKey,
} from '@/lib/api/generated';
import type { PlaylistDTO, PlaylistBody } from '@/lib/api/model';
import { citronCard, citronBadge, inputClass, primaryBtn, rowClass } from './ui';

// Pull the bare playlist id out of a share URL / URI so it can be matched
// against PlaylistDTO.spotify_id.
function spotifyIdFromUrl(url: string | undefined): string | null {
  if (!url) return null;
  const m = url.match(/playlist[/:]([A-Za-z0-9]+)/);
  return m ? m[1] : null;
}

interface Meta {
  name: string;
  image?: string;
  url?: string;
}

export function PlaylistsSection() {
  const queryClient = useQueryClient();
  const { data, isLoading } = useListPlaylists();
  const { data: publicData } = useGetSpotifyPlaylists();

  // spotify_id -> display metadata, built from the public playlists payload.
  const metaById = new Map<string, Meta>();
  for (const p of publicData?.playlists ?? ([] as PlaylistBody[])) {
    const id = spotifyIdFromUrl(p.url);
    if (id) metaById.set(id, { name: p.name, image: p.image, url: p.url });
  }

  const playlists = [...(data?.playlists ?? [])].sort(
    (a, b) => a.sort_order - b.sort_order || a.id - b.id,
  );

  const [input, setInput] = useState('');

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: getListPlaylistsQueryKey() });

  const create = useCreatePlaylist();
  const remove = useDeletePlaylist();
  const reorder = useReorderPlaylists();

  const canAdd = input.trim().length > 0 && !create.isPending;

  const add = () => {
    if (!canAdd) return;
    create.mutate(
      { data: { input: input.trim() } },
      {
        onSuccess: () => {
          invalidate();
          setInput('');
        },
      },
    );
  };

  const del = (p: PlaylistDTO) => {
    const meta = metaById.get(p.spotify_id);
    if (!confirm(`Remove "${meta?.name ?? p.spotify_id}" from the curated set?`))
      return;
    remove.mutate({ id: p.id }, { onSuccess: invalidate });
  };

  // Move up/down: swap with the neighbour, then renumber sort_order to the new
  // index for the whole set and persist in one reorder call.
  const move = (index: number, dir: -1 | 1) => {
    const j = index + dir;
    if (j < 0 || j >= playlists.length) return;
    const next = [...playlists];
    [next[index], next[j]] = [next[j], next[index]];
    reorder.mutate(
      { data: { items: next.map((p, i) => ({ id: p.id, sort_order: i })) } },
      { onSuccess: invalidate },
    );
  };

  const busy = reorder.isPending || remove.isPending;

  return (
    <section
      className="flex flex-col gap-5 rounded-2xl border p-5 sm:p-6"
      style={citronCard}
    >
      <header className="flex items-center gap-3">
        <span
          className="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-xl text-citron"
          style={citronBadge}
        >
          <PlaylistGlyph />
        </span>
        <div>
          <h2 className="font-display text-lg font-bold text-white">
            Playlists
          </h2>
          <p className="text-sm text-slate-400">
            The curated set shown in the Music panel.
          </p>
        </div>
      </header>

      {/* Add by URL / URI / ID */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
        <label className="flex flex-1 flex-col gap-1 text-sm text-slate-300">
          Spotify playlist
          <input
            type="text"
            className={inputClass}
            placeholder="Share URL, spotify:playlist:… URI, or bare ID"
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                add();
              }
            }}
          />
        </label>
        <button
          type="button"
          className={primaryBtn}
          disabled={!canAdd}
          onClick={add}
        >
          {create.isPending ? 'Adding…' : 'Add'}
        </button>
      </div>

      {create.error ? (
        <p className="text-sm text-coral">{(create.error as Error).message}</p>
      ) : null}

      {/* Curated list */}
      {isLoading ? (
        <p className="text-sm text-slate-400">Loading…</p>
      ) : playlists.length === 0 ? (
        <p className="text-sm text-slate-400">No curated playlists yet.</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {playlists.map((p, i) => {
            const meta = metaById.get(p.spotify_id);
            return (
              <li key={p.id} className={rowClass}>
                <div className="flex min-w-0 items-center gap-3">
                  {meta?.image ? (
                    // Remote Spotify CDN art — plain img avoids next/image config.
                    // eslint-disable-next-line @next/next/no-img-element
                    <img
                      src={meta.image}
                      alt=""
                      className="h-10 w-10 shrink-0 rounded-sm object-cover"
                    />
                  ) : (
                    <div className="h-10 w-10 shrink-0 rounded-sm bg-sky/10" />
                  )}
                  <div className="min-w-0">
                    <p className="truncate font-medium text-white">
                      {meta?.name ?? p.spotify_id}
                    </p>
                    <p className="truncate font-mono text-xs text-slate-400">
                      {p.spotify_id}
                    </p>
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  <button
                    type="button"
                    aria-label="Move up"
                    disabled={i === 0 || busy}
                    onClick={() => move(i, -1)}
                    className="rounded-md px-2 py-1 text-slate-300 transition hover:text-white disabled:opacity-30"
                  >
                    ↑
                  </button>
                  <button
                    type="button"
                    aria-label="Move down"
                    disabled={i === playlists.length - 1 || busy}
                    onClick={() => move(i, 1)}
                    className="rounded-md px-2 py-1 text-slate-300 transition hover:text-white disabled:opacity-30"
                  >
                    ↓
                  </button>
                  <button
                    type="button"
                    onClick={() => del(p)}
                    disabled={busy}
                    className="ml-1 text-sm text-coral transition hover:brightness-110 disabled:opacity-50"
                  >
                    Remove
                  </button>
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

function PlaylistGlyph() {
  return (
    <svg
      width="18"
      height="18"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M9 18V5l12-2v13" />
      <circle cx="6" cy="18" r="3" />
      <circle cx="18" cy="16" r="3" />
    </svg>
  );
}
