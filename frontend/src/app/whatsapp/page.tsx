'use client';

// The WhatsApp Sender tool (ADR 11), gated to the friend and admin tiers. The
// gate here is UX only; the backend re-enforces it on every request (a member
// who reached this page still gets 403s). See ADR 10.

import { useAuth } from '@/lib/auth';
import { TemplatesPanel } from '@/components/whatsapp/TemplatesPanel';
import { ListsPanel } from '@/components/whatsapp/ListsPanel';
import { SendPanel } from '@/components/whatsapp/SendPanel';

export default function WhatsAppPage() {
  const { isLoading, isAuthenticated, isFriend, signIn } = useAuth();

  return (
    <main className="mx-auto flex min-h-screen w-full max-w-2xl flex-col gap-8 px-6 py-16">
      <header className="flex flex-col gap-2">
        <h1 className="text-2xl font-bold text-citron">WhatsApp Sender</h1>
        <p className="text-sm text-slate-400">
          Link your own WhatsApp, save a message template and a list of numbers,
          then send a personalized batch. Capped at 250 recipients and 3 batches a
          day.
        </p>
      </header>

      {isLoading ? (
        <p className="text-slate-400">Loading…</p>
      ) : !isAuthenticated ? (
        <div className="flex flex-col gap-4">
          <p className="text-slate-400">Sign in to use this tool.</p>
          <button
            type="button"
            onClick={signIn}
            className="self-start rounded-md bg-citron px-5 py-2.5 font-semibold text-deepsea transition hover:brightness-95"
          >
            Sign in with Google
          </button>
        </div>
      ) : !isFriend ? (
        <p className="rounded-md border border-slate-700 bg-deepsea/60 p-4 text-slate-300">
          This tool is available to friends only. If you think you should have
          access, get in touch.
        </p>
      ) : (
        <div className="flex flex-col gap-10">
          <SendPanel />
          <div className="h-px bg-slate-800" />
          <TemplatesPanel />
          <div className="h-px bg-slate-800" />
          <ListsPanel />
        </div>
      )}
    </main>
  );
}
