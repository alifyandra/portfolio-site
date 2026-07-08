'use client';

import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';

import {
  useUpdateCurrentUser,
  getGetCurrentUserQueryKey,
} from '@/lib/api/generated';
import type { UserOutputBody } from '@/lib/api/model';
import { useAuth, type Role } from '@/lib/auth';

const roleLabel: Record<Role, string> = {
  admin: 'Admin',
  friend: 'Friend',
  member: 'Member',
};

const roleBlurb: Record<Role, string> = {
  admin: 'You have access to everything.',
  friend: 'You have access to friends-only tools.',
  member: 'You have access to the public tools.',
};

// Role identity colours, matching the navbar chip (admin=citron, friend=mint,
// member=sky).
const roleText: Record<Role, string> = {
  admin: 'text-citron',
  friend: 'text-mint',
  member: 'text-sky',
};
const roleDot: Record<Role, string> = {
  admin: 'bg-citron',
  friend: 'bg-mint',
  member: 'bg-sky',
};

// Citron-tinted card, matching CountryCodeSetting so the account surfaces read
// as one family.
const cardStyle = {
  borderColor: 'color-mix(in srgb, var(--color-citron) 32%, transparent)',
  background: 'color-mix(in srgb, var(--color-citron) 6%, var(--color-deepsea))',
};

export default function AccountPage() {
  const { user, role, displayName, isLoading, isAuthenticated, signIn, signOut } =
    useAuth();

  return (
    <main className="mx-auto flex min-h-screen w-full max-w-md flex-col px-6 pb-16 pt-24">
      <p className="font-mono text-xs uppercase tracking-widest text-mint">
        account
      </p>
      <h1 className="section-title mt-2 font-display text-3xl font-bold sm:text-4xl">
        Your account
      </h1>

      <div className="mt-8">
        {isLoading ? (
          <p className="text-slate-400">Loading…</p>
        ) : isAuthenticated && user ? (
          <div className="flex flex-col gap-6">
            <div className="flex items-center gap-4">
              {user.avatar_url ? (
                // Plain img: Google avatars are remote and need no Next/Image config.
                // eslint-disable-next-line @next/next/no-img-element
                <img
                  src={user.avatar_url}
                  alt=""
                  width={56}
                  height={56}
                  referrerPolicy="no-referrer"
                  className="h-14 w-14 rounded-full"
                />
              ) : null}
              <div className="min-w-0">
                <p className="truncate font-display text-lg font-bold text-white">
                  {displayName || user.email}
                </p>
                <p className="truncate text-sm text-slate-400">{user.email}</p>
              </div>
            </div>

            {role ? (
              <div className="flex items-center gap-2">
                <span
                  aria-hidden
                  className={`h-2.5 w-2.5 rounded-full ${roleDot[role]}`}
                />
                <span
                  className={`font-mono text-xs uppercase tracking-widest ${roleText[role]}`}
                >
                  {roleLabel[role]}
                </span>
                <span className="text-sm text-slate-400">
                  · {roleBlurb[role]}
                </span>
              </div>
            ) : null}

            <NicknameEditor user={user} />

            <button
              type="button"
              onClick={signOut}
              className="self-start rounded-md border border-slate-700 px-5 py-2.5 font-semibold text-white transition hover:border-coral hover:text-coral"
            >
              Sign out
            </button>
          </div>
        ) : (
          <div
            className="flex flex-col gap-5 rounded-2xl border p-6 sm:p-8"
            style={cardStyle}
          >
            <p className="text-slate-300">
              Sign in to set up your profile and open the tools meant for you.
            </p>
            <button
              type="button"
              onClick={signIn}
              className="self-start rounded-md bg-citron px-5 py-2.5 font-semibold text-ink transition hover:brightness-95"
            >
              Sign in with Google
            </button>
          </div>
        )}
      </div>
    </main>
  );
}

// The Nickname editor. Same mutation + query-invalidation pattern as
// CountryCodeSetting; sends ONLY `nickname` (never default_country_code) so the
// stored country code is left untouched. Empty clears the nickname (sends null).
function NicknameEditor({ user }: { user: UserOutputBody }) {
  const queryClient = useQueryClient();
  const update = useUpdateCurrentUser();
  const [value, setValue] = useState(user.nickname ?? '');
  const [justSaved, setJustSaved] = useState(false);

  const trimmed = value.trim();
  const dirty = trimmed !== (user.nickname ?? '');

  const save = () => {
    if (!dirty || update.isPending) return;
    update.mutate(
      { data: { nickname: trimmed ? trimmed : null } },
      {
        onSuccess: () => {
          queryClient.invalidateQueries({
            queryKey: getGetCurrentUserQueryKey(),
          });
          setJustSaved(true);
        },
      },
    );
  };

  return (
    <section
      className="flex flex-col gap-3 rounded-2xl border p-4 sm:p-5"
      style={cardStyle}
    >
      <p className="font-mono text-xs uppercase tracking-widest text-citron">
        nickname
      </p>
      <label className="flex flex-col gap-1 text-sm text-slate-300">
        What you’d like to be called
        <div className="flex flex-col gap-2 sm:flex-row">
          <input
            type="text"
            value={value}
            maxLength={40}
            placeholder={user.name || 'Your name'}
            aria-label="Nickname"
            onChange={(e) => {
              setValue(e.target.value);
              setJustSaved(false);
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                save();
              }
            }}
            className="min-w-0 flex-1 rounded-lg border border-slate-700 bg-deepsea px-3 py-2 text-white outline-none transition focus:border-sky"
          />
          <button
            type="button"
            onClick={save}
            disabled={!dirty || update.isPending}
            className="rounded-md bg-citron px-4 py-2 text-sm font-semibold text-ink transition hover:brightness-95 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {update.isPending ? 'Saving…' : 'Save'}
          </button>
        </div>
      </label>
      <p className="text-xs text-slate-400">
        Overrides your Google name wherever you’re shown. Leave it empty to go
        back to {user.name || 'your Google name'}.
      </p>
      {justSaved && !update.isPending ? (
        <p className="font-mono text-xs uppercase tracking-widest text-mint">
          saved ✓
        </p>
      ) : null}
      {update.error ? (
        <p className="text-sm text-coral">{(update.error as Error).message}</p>
      ) : null}
    </section>
  );
}
