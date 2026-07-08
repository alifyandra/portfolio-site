'use client';

// Friends section of the Admin Console: view / grant / revoke AccessGrants
// (ADR 10 amendment + ADR 12). Writes go to /api/admin/access-grants behind the
// server-enforced admin middleware; this UI is admin-gated for UX only.
//
// Effective role is max(env allowlist, DB grant): the ADMIN_EMAILS /
// FRIEND_EMAILS env allowlists are a permanent floor and are NOT represented as
// grants here, so they can't be listed or revoked from this screen.

import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';

import {
  useListAccessGrants,
  useCreateAccessGrant,
  useDeleteAccessGrant,
  getListAccessGrantsQueryKey,
} from '@/lib/api/generated';
import { CreateGrantInputBodyTier } from '@/lib/api/model';
import { citronCard, citronBadge, inputClass, primaryBtn, rowClass } from './ui';

const tierText: Record<string, string> = {
  admin: 'text-citron',
  friend: 'text-mint',
};

function formatDate(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? iso
    : d.toLocaleDateString(undefined, {
        year: 'numeric',
        month: 'short',
        day: 'numeric',
      });
}

export function FriendsSection() {
  const queryClient = useQueryClient();
  const { data, isLoading } = useListAccessGrants();
  const grants = data?.grants ?? [];

  const [email, setEmail] = useState('');
  const [tier, setTier] = useState<CreateGrantInputBodyTier>(
    CreateGrantInputBodyTier.friend,
  );

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: getListAccessGrantsQueryKey() });

  const create = useCreateAccessGrant();
  const remove = useDeleteAccessGrant();

  const canAdd = email.trim().length >= 3 && !create.isPending;

  const add = () => {
    if (!canAdd) return;
    create.mutate(
      { data: { email: email.trim().toLowerCase(), tier } },
      {
        onSuccess: () => {
          invalidate();
          setEmail('');
          setTier(CreateGrantInputBodyTier.friend);
        },
      },
    );
  };

  const revoke = (grantEmail: string) => {
    if (!confirm(`Revoke access for ${grantEmail}?`)) return;
    remove.mutate({ email: grantEmail }, { onSuccess: invalidate });
  };

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
          <FriendsGlyph />
        </span>
        <div>
          <h2 className="font-display text-lg font-bold text-white">Friends</h2>
          <p className="text-sm text-slate-400">
            Grant or revoke access tiers by email.
          </p>
        </div>
      </header>

      {/* Add grant */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
        <label className="flex flex-1 flex-col gap-1 text-sm text-slate-300">
          Email
          <input
            type="email"
            inputMode="email"
            autoComplete="off"
            className={inputClass}
            placeholder="person@example.com"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                add();
              }
            }}
          />
        </label>
        <label className="flex flex-col gap-1 text-sm text-slate-300 sm:w-40">
          Tier
          <select
            className={inputClass}
            value={tier}
            onChange={(e) =>
              setTier(e.target.value as CreateGrantInputBodyTier)
            }
          >
            <option value={CreateGrantInputBodyTier.friend}>friend</option>
            <option value={CreateGrantInputBodyTier.admin}>admin</option>
          </select>
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

      {/* Grants list */}
      {isLoading ? (
        <p className="text-sm text-slate-400">Loading…</p>
      ) : grants.length === 0 ? (
        <p className="text-sm text-slate-400">No grants yet.</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {grants.map((g) => (
            <li key={g.email} className={rowClass}>
              <div className="min-w-0">
                <p className="truncate font-medium text-white">{g.email}</p>
                <p className="mt-0.5 text-xs text-slate-400">
                  <span
                    className={`font-mono uppercase tracking-widest ${
                      tierText[g.tier] ?? 'text-slate-300'
                    }`}
                  >
                    {g.tier}
                  </span>
                  <span> · granted {formatDate(g.created_at)}</span>
                </p>
              </div>
              <button
                type="button"
                onClick={() => revoke(g.email)}
                disabled={remove.isPending}
                className="shrink-0 text-sm text-coral transition hover:brightness-110 disabled:opacity-50"
              >
                Revoke
              </button>
            </li>
          ))}
        </ul>
      )}

      <p className="text-xs text-slate-400">
        Env-allowlisted admins and friends are a permanent floor (effective role
        is the max of the env allowlist and any grant here) and are not shown or
        removable from this list.
      </p>
    </section>
  );
}

function FriendsGlyph() {
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
      <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
      <circle cx="9" cy="7" r="4" />
      <path d="M22 21v-2a4 4 0 0 0-3-3.87M16 3.13A4 4 0 0 1 16 11" />
    </svg>
  );
}
