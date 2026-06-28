'use client';

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

export default function AccountPage() {
  const { user, role, isLoading, isAuthenticated, signIn, signOut } = useAuth();

  return (
    <main className="mx-auto flex min-h-screen w-full max-w-md flex-col justify-center px-6 py-16">
      <h1 className="mb-8 text-2xl font-bold text-citron">Account</h1>

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
              <p className="truncate font-semibold text-white">
                {user.name || user.email}
              </p>
              <p className="truncate text-sm text-slate-400">{user.email}</p>
            </div>
          </div>

          {role ? (
            <div>
              <span className="inline-block rounded-full border border-slate-700 bg-deepsea px-3 py-1 text-sm font-medium text-sky">
                {roleLabel[role]}
              </span>
              <p className="mt-2 text-sm text-slate-400">{roleBlurb[role]}</p>
            </div>
          ) : null}

          <button
            type="button"
            onClick={signOut}
            className="self-start rounded-md border border-slate-700 px-5 py-2.5 font-semibold text-white transition hover:border-coral hover:text-coral"
          >
            Sign out
          </button>
        </div>
      ) : (
        <div className="flex flex-col gap-4">
          <p className="text-slate-400">Sign in to access your tools.</p>
          <button
            type="button"
            onClick={signIn}
            className="self-start rounded-md bg-citron px-5 py-2.5 font-semibold text-deepsea transition hover:brightness-95"
          >
            Sign in with Google
          </button>
        </div>
      )}
    </main>
  );
}
