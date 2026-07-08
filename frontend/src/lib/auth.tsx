'use client';

// Client-side auth state. The backend owns the OAuth flow and the opaque
// session cookie (see ADR 10); the frontend only reads "who am I" from
// GET /api/auth/me and offers sign-in / sign-out affordances. This is UX only,
// never the security boundary, which is enforced server-side.

import { createContext, useContext, type ReactNode } from 'react';
import { useQueryClient } from '@tanstack/react-query';

import {
  useGetCurrentUser,
  useLogout,
  getGetCurrentUserQueryKey,
} from '@/lib/api/generated';
import type { UserOutputBody } from '@/lib/api/model';
import { BASE_URL } from '@/lib/fetcher';

// Mirrors the backend User.role enum (ADR 10 tiered access model).
export type Role = 'admin' | 'friend' | 'member';

interface AuthContextValue {
  user: UserOutputBody | null;
  role: Role | null;
  /**
   * What to call the user in the UI: their self-chosen Nickname, else the
   * provider `name`, else the email (precedence nickname ?? name ?? email;
   * see CONTEXT.md). Empty string when logged out.
   */
  displayName: string;
  isLoading: boolean;
  isAuthenticated: boolean;
  /** True for friend or admin: the tiers with access to gated tools. */
  isFriend: boolean;
  isAdmin: boolean;
  signIn: () => void;
  signOut: () => void;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();

  // /api/auth/me returns 401 for anonymous visitors, which our fetcher turns
  // into a thrown error. Don't retry that (it's a normal "logged out" state).
  const { data, isLoading } = useGetCurrentUser({
    query: { retry: false, staleTime: 30_000 },
  });
  const { mutate: doLogout } = useLogout();

  const user = data ?? null;
  const role = (user?.role as Role | undefined) ?? null;
  const displayName = user
    ? (user.nickname ?? user.name ?? user.email ?? '')
    : '';

  const signIn = () => {
    // Full-page navigation, not fetch: the backend runs the Google redirect
    // dance and sets the cookie, then redirects back to FRONTEND_URL.
    window.location.href = `${BASE_URL}/api/auth/google/login`;
  };

  const signOut = () => {
    doLogout(undefined, {
      onSettled: () => {
        // The server cleared the session cookie; drop the cached identity so the
        // UI flips to logged-out without a manual refresh. We remove the query
        // rather than setQueryData(undefined) (which React Query ignores) because
        // a 401 refetch would otherwise retain the last successful user as `data`.
        queryClient.removeQueries({ queryKey: getGetCurrentUserQueryKey() });
      },
    });
  };

  return (
    <AuthContext.Provider
      value={{
        user,
        role,
        displayName,
        isLoading,
        isAuthenticated: user !== null,
        isFriend: role === 'friend' || role === 'admin',
        isAdmin: role === 'admin',
        signIn,
        signOut,
      }}
    >
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) {
    throw new Error('useAuth must be used within <AuthProvider>');
  }
  return ctx;
}
