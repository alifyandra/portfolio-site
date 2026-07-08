'use client';

// Global navbar, rendered on every page from layout.tsx. Sticky + in flow, so
// it pushes page content down naturally (no fixed overlap). Semi-transparent
// canvas + backdrop-blur so the hero's ambient shapes read faintly through it.
// About lives in the landing teaser + footer, not here.

import Link from 'next/link';
import { useAuth, type Role } from '@/lib/auth';
import { BrandMark } from './BrandMark';
import { ThemeToggle } from './ThemeToggle';
import { AppMenu } from './AppMenu';

// Role identity on the account chip: admin=citron, friend=mint, member=sky.
const roleDot: Record<Role, string> = {
  admin: 'bg-citron',
  friend: 'bg-mint',
  member: 'bg-sky',
};
const roleText: Record<Role, string> = {
  admin: 'text-citron',
  friend: 'text-mint',
  member: 'text-sky',
};

// Dedicated Admin entry, rendered only for admins and tinted citron (the admin
// accent). Deliberately sits next to the Apps menu rather than inside it: the
// console is an admin surface, not a Tool. Backend re-enforces the real gate.
function AdminLink() {
  const { isAdmin } = useAuth();
  if (!isAdmin) return null;
  return (
    <Link
      href="/admin"
      className="flex items-center gap-1.5 rounded-md px-2 py-1 text-sm font-medium text-citron no-underline transition hover:brightness-110"
    >
      <span aria-hidden className="h-2 w-2 rounded-full bg-citron" />
      <span className="hidden sm:inline">Admin</span>
    </Link>
  );
}

function AccountControl() {
  const { user, role, displayName, isLoading, isAuthenticated, signIn } =
    useAuth();

  // Skip rendering until we know who the visitor is, to avoid a sign-in ->
  // chip flicker on load.
  if (isLoading) {
    return <div className="h-9 w-9" aria-hidden />;
  }

  if (isAuthenticated && user) {
    const shown = displayName || user.email;
    return (
      <Link
        href="/account"
        aria-label={`Account — ${shown}${role ? `, ${role}` : ''}`}
        className="flex items-center gap-2 rounded-full border border-slate-700 bg-deepsea py-1 pl-1 pr-3 text-sm text-slate-200 no-underline transition hover:border-slate-500 hover:text-white"
      >
        <span className="relative inline-flex">
          {user.avatar_url ? (
            // Plain img: Google avatars are remote and need no Next/Image config.
            // eslint-disable-next-line @next/next/no-img-element
            <img
              src={user.avatar_url}
              alt=""
              width={28}
              height={28}
              referrerPolicy="no-referrer"
              className="h-7 w-7 rounded-full"
            />
          ) : (
            <span className="flex h-7 w-7 items-center justify-center rounded-full bg-slate-700 text-xs font-semibold text-white">
              {shown.charAt(0).toUpperCase()}
            </span>
          )}
          {role ? (
            <span
              aria-hidden
              className={`absolute -bottom-0.5 -right-0.5 h-2.5 w-2.5 rounded-full border-2 border-deepsea ${roleDot[role]}`}
            />
          ) : null}
        </span>
        {/* Display name over a small role label. Hidden on the narrowest widths
            so the chip stays compact; the avatar + dot still convey identity. */}
        <span className="hidden min-w-0 flex-col leading-tight sm:flex">
          <span className="max-w-[9rem] truncate font-medium text-white">
            {shown}
          </span>
          {role ? (
            <span
              className={`font-mono text-[0.6rem] uppercase tracking-widest ${roleText[role]}`}
            >
              {role}
            </span>
          ) : null}
        </span>
      </Link>
    );
  }

  return (
    <button
      type="button"
      onClick={signIn}
      className="rounded-md bg-citron px-3.5 py-1.5 text-sm font-semibold text-ink transition hover:brightness-95"
    >
      Sign in
    </button>
  );
}

export function Navbar() {
  return (
    <nav
      className="sticky top-0 z-40 w-full border-b border-slate-800 backdrop-blur"
      style={{
        backgroundColor: 'color-mix(in srgb, var(--canvas) 82%, transparent)',
      }}
    >
      <div className="mx-auto flex w-full max-w-4xl items-center justify-between gap-3 px-4 py-3 sm:px-6">
        <Link href="/" className="no-underline" aria-label="aliflabs home">
          <BrandMark className="text-xl" />
        </Link>

        <div className="flex items-center gap-2 sm:gap-3">
          <AdminLink />
          <AppMenu />
          <ThemeToggle />
          <AccountControl />
        </div>
      </div>
    </nav>
  );
}
