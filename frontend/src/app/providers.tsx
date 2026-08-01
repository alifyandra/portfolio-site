'use client';

import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';

import { AuthProvider } from '@/lib/auth';
import { CensorProvider } from '@/components/finance/censor';

export function Providers({ children }: { children: ReactNode }) {
  const [client] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            staleTime: 30_000,
            refetchOnWindowFocus: false,
          },
        },
      }),
  );

  return (
    <QueryClientProvider client={client}>
      <AuthProvider>
        {/* Sits at the root so the finance censor survives client-side
            navigation between /finance and /admin. It is not persisted, so a
            hard reload puts it back to censored on purpose. */}
        <CensorProvider>{children}</CensorProvider>
      </AuthProvider>
    </QueryClientProvider>
  );
}
