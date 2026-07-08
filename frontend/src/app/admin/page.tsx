'use client';

// The Admin Console (ADR 10 amendment + ADR 12): admin-only management for
// Projects, Friends (AccessGrants) and Playlists. The gate here is UX only;
// every /api/admin/* write is re-enforced by the server-side admin middleware,
// so a non-admin who reaches this page still gets 403s. Mirrors the client-side
// gate shape used by /whatsapp.

import { useState } from 'react';

import { useAuth } from '@/lib/auth';
import { ProjectsSection } from '@/components/admin/ProjectsSection';
import { FriendsSection } from '@/components/admin/FriendsSection';
import { PlaylistsSection } from '@/components/admin/PlaylistsSection';
import { citronCard, citronBadge } from '@/components/admin/ui';

const tabs = [
  { id: 'projects', label: 'Projects' },
  { id: 'friends', label: 'Friends' },
  { id: 'playlists', label: 'Playlists' },
] as const;

type TabId = (typeof tabs)[number]['id'];

export default function AdminPage() {
  const { isLoading, isAuthenticated, isAdmin, signIn } = useAuth();
  const [tab, setTab] = useState<TabId>('projects');

  return (
    <main className="mx-auto flex min-h-screen w-full max-w-4xl flex-col gap-8 px-6 pb-24 pt-12">
      <header className="flex flex-col gap-3">
        <p className="font-mono text-sm lowercase tracking-wide text-citron">
          aliflabs · admin
        </p>
        <h1 className="font-display text-3xl font-bold sm:text-4xl">
          <span className="text-white">Admin </span>
          <span className="text-citron">Console</span>
        </h1>
        <p className="max-w-xl leading-relaxed text-slate-300">
          Manage portfolio projects, friend access grants and the curated
          Spotify playlist set.
        </p>
      </header>

      {isLoading ? (
        <div className="flex items-center gap-2 text-sm text-slate-400">
          <span className="h-2 w-2 animate-pulse rounded-full bg-citron" />
          Loading…
        </div>
      ) : !isAuthenticated ? (
        <div
          className="flex flex-col items-start gap-4 rounded-2xl border p-6 sm:p-8"
          style={citronCard}
        >
          <span
            className="inline-flex h-11 w-11 items-center justify-center rounded-xl text-citron"
            style={citronBadge}
          >
            <LockGlyph />
          </span>
          <div className="flex flex-col gap-1">
            <h2 className="font-display text-xl font-bold text-white">
              Sign in to continue
            </h2>
            <p className="text-slate-300">This area is for the site admin.</p>
          </div>
          <button
            type="button"
            onClick={signIn}
            className="rounded-lg bg-citron px-5 py-2.5 font-semibold text-ink transition hover:brightness-95"
          >
            Sign in with Google
          </button>
        </div>
      ) : !isAdmin ? (
        <div
          className="flex flex-col items-start gap-3 rounded-2xl border p-6"
          style={{
            borderColor: 'color-mix(in srgb, var(--color-coral) 42%, transparent)',
            background:
              'color-mix(in srgb, var(--color-coral) 8%, var(--color-deepsea))',
          }}
        >
          <span
            className="inline-flex h-11 w-11 items-center justify-center rounded-xl text-coral"
            style={{
              background: 'color-mix(in srgb, var(--color-coral) 16%, transparent)',
            }}
          >
            <LockGlyph />
          </span>
          <p className="font-mono text-xs uppercase tracking-widest text-coral">
            admins only
          </p>
          <p className="text-slate-300">
            This area is restricted to the site admin. If you think you should
            have access, get in touch.
          </p>
        </div>
      ) : (
        <div className="flex flex-col gap-6">
          {/* Tabs */}
          <div
            role="tablist"
            aria-label="Admin sections"
            className="flex gap-1 border-b border-slate-800"
          >
            {tabs.map((t) => {
              const active = t.id === tab;
              return (
                <button
                  key={t.id}
                  type="button"
                  role="tab"
                  aria-selected={active}
                  onClick={() => setTab(t.id)}
                  className={`-mb-px border-b-2 px-4 py-2 text-sm font-medium transition ${
                    active
                      ? 'border-citron text-citron'
                      : 'border-transparent text-slate-400 hover:text-white'
                  }`}
                >
                  {t.label}
                </button>
              );
            })}
          </div>

          {tab === 'projects' && <ProjectsSection />}
          {tab === 'friends' && <FriendsSection />}
          {tab === 'playlists' && <PlaylistsSection />}
        </div>
      )}
    </main>
  );
}

function LockGlyph() {
  return (
    <svg
      width="22"
      height="22"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.9"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <rect x="4" y="10.5" width="16" height="10" rx="2" />
      <path d="M8 10.5V7a4 4 0 0 1 8 0v3.5" />
    </svg>
  );
}
