'use client';

// The Welcome (see CONTEXT.md): the post-sign-in moment. Mounted globally in
// layout.tsx inside <Providers> so it has auth context. It is pure UX — it
// never gates anything (the server enforces Role independently).
//
// It greets only when the server's greeted_role is *behind* the user's current
// situation, i.e. there is something new to acknowledge. A caught-up visitor
// (greeted_role already covers the current role) is RETURNING and shows
// nothing, so a cached session (opening the site in a new tab, a refresh, a
// revisit) never replays the greeting. greeted_role is server-authoritative,
// so this holds across tabs and devices without any client-side "shown" flag.
//
// The outcome is decided from server state when auth resolves. ONBOARD, NAMED,
// and PROMOTED are the Kinds that render; RETURNING is not a Kind — it is the
// no-op where the effect shows nothing (state stays null):
//   ONBOARD    — never welcomed (greeted_role == null) AND no nickname yet:
//                ask for a Nickname, then play the FULL greeting. Submit/skip
//                PATCHes ack_welcome, settling to a silent RETURNING.
//   NAMED      — never welcomed (greeted_role == null) but a nickname already
//                exists (set on /account, or on another device where the ack
//                did not land): do NOT re-ask. Play a SHORT greeting using the
//                existing name and PATCH ack_welcome, settling to a silent
//                RETURNING.
//   PROMOTED   — current role outranks the role we last greeted at (a
//                promotion, e.g. member→friend or friend→admin): play the FULL
//                greeting (with the "you are my friend" beat), then PATCH
//                ack_welcome so the celebration shows once.
//   RETURNING  — otherwise (greeted_role already covers the current role): show
//                nothing. This is the everyday cached-session case (new tab,
//                refresh, revisit).

import { useCallback, useEffect, useRef, useState } from 'react';
import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import { useQueryClient } from '@tanstack/react-query';

import {
  useUpdateCurrentUser,
  getGetCurrentUserQueryKey,
} from '@/lib/api/generated';
import { useAuth, type Role } from '@/lib/auth';

type Kind = 'onboard' | 'named' | 'promoted';
type Phase = 'prompt' | 'greeting';
// The whole Welcome is held in one state object so the decision effect makes a
// single state write; null means nothing is showing.
type WState = { kind: Kind; phase: Phase; name: string };

// Access tiers ranked low→high (ADR 10: admin > friend > member). A promotion
// is the current role outranking the role we last greeted at, so any upgrade
// (member→friend, friend→admin, member→admin) is caught, not just member→*.
const roleRank = (r: Role | null | undefined): number =>
  r === 'admin' ? 2 : r === 'friend' ? 1 : 0;

// Shared theme-aware overlay wash (palette glows over the page canvas). Uses
// CSS vars so it reads right in both dark and light themes.
const overlayBg = {
  background: [
    'radial-gradient(55% 55% at 28% 30%, color-mix(in srgb, var(--color-sky) 30%, transparent), transparent 70%)',
    'radial-gradient(52% 52% at 78% 70%, color-mix(in srgb, var(--color-mint) 26%, transparent), transparent 70%)',
    'radial-gradient(46% 46% at 66% 22%, color-mix(in srgb, var(--color-citron) 20%, transparent), transparent 70%)',
    'color-mix(in srgb, var(--canvas) 90%, transparent)',
  ].join(', '),
  backdropFilter: 'blur(6px)',
  WebkitBackdropFilter: 'blur(6px)',
} as const;

export function Welcome() {
  const { isAuthenticated, isLoading, user, isFriend } = useAuth();
  const queryClient = useQueryClient();
  const update = useUpdateCurrentUser();

  const [state, setState] = useState<WState | null>(null);
  const decidedRef = useRef(false);

  const invalidate = useCallback(() => {
    queryClient.invalidateQueries({ queryKey: getGetCurrentUserQueryKey() });
  }, [queryClient]);

  // Decide once, when auth first resolves to a signed-in user. This effect
  // subscribes the Welcome to the async auth query and makes a single decision
  // when it settles, so one synchronous setState here is intended.
  useEffect(() => {
    if (decidedRef.current) return;
    if (isLoading || !isAuthenticated || !user) return;
    decidedRef.current = true;

    const greeted = user.greeted_role;
    const hasNickname = Boolean(user.nickname);
    const display = user.nickname ?? user.name ?? '';
    // A promotion is the current role outranking the role we last greeted at.
    // Guard on greeted != null so this stands on its own (the never-greeted
    // null cases are handled as ONBOARD/NAMED below, not here).
    const promoted =
      greeted != null && roleRank(user.role) > roleRank(greeted);

    // Greet only when greeted_role is behind the current situation. A caught-up
    // (RETURNING) user shows nothing, so cached loads never replay the greeting.
    let next: WState | null = null;
    if (greeted == null && !hasNickname) {
      // Brand new: ask for a name, then the full greeting.
      next = { kind: 'onboard', phase: 'prompt', name: user.name ?? '' };
    } else if (greeted == null) {
      // Already named but never acked (e.g. nickname set on /account, or a
      // prior device whose ack did not land). Don't re-prompt — greet by the
      // existing name and ack on completion (see handleGreetingDone).
      next = { kind: 'named', phase: 'greeting', name: display };
    } else if (promoted) {
      next = { kind: 'promoted', phase: 'greeting', name: display };
    }
    // else RETURNING: greeted_role already covers this role — stay silent.

    if (next) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setState(next);
    }
  }, [isLoading, isAuthenticated, user]);

  const handleSubmit = (value: string) => {
    const trimmed = value.trim();
    if (trimmed) {
      update.mutate(
        { data: { nickname: trimmed, ack_welcome: true } },
        { onSuccess: invalidate },
      );
    } else {
      // Empty submit is treated as a skip: acknowledge without a nickname.
      update.mutate({ data: { ack_welcome: true } }, { onSuccess: invalidate });
    }
    setState((s) =>
      s
        ? {
            ...s,
            phase: 'greeting',
            name: trimmed || user?.nickname || user?.name || '',
          }
        : s,
    );
  };

  const handleSkip = () => {
    update.mutate({ data: { ack_welcome: true } }, { onSuccess: invalidate });
    setState((s) =>
      s
        ? { ...s, phase: 'greeting', name: user?.nickname ?? user?.name ?? '' }
        : s,
    );
  };

  const handleGreetingDone = useCallback(() => {
    // PROMOTED and NAMED ack after the greeting so it plays once and settles to
    // RETURNING. ONBOARD already acked at submit/skip; RETURNING never acks.
    setState((s) => {
      if (s?.kind === 'promoted' || s?.kind === 'named') {
        update.mutate(
          { data: { ack_welcome: true } },
          { onSuccess: invalidate },
        );
      }
      return null;
    });
  }, [update, invalidate]);

  if (!user || !state) return null;

  return (
    <AnimatePresence>
      {state.phase === 'prompt' ? (
        <NicknamePrompt
          key="prompt"
          defaultName={user.name ?? ''}
          pending={update.isPending}
          onSubmit={handleSubmit}
          onSkip={handleSkip}
        />
      ) : (
        <Greeting
          key="greeting"
          name={state.name}
          isFriend={isFriend}
          full={state.kind === 'onboard' || state.kind === 'promoted'}
          onDone={handleGreetingDone}
        />
      )}
    </AnimatePresence>
  );
}

// ── Nickname prompt ────────────────────────────────────────────────────────

function NicknamePrompt({
  defaultName,
  pending,
  onSubmit,
  onSkip,
}: {
  defaultName: string;
  pending: boolean;
  onSubmit: (value: string) => void;
  onSkip: () => void;
}) {
  const [value, setValue] = useState(defaultName);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
    inputRef.current?.select();
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onSkip();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onSkip]);

  return (
    <motion.div
      role="dialog"
      aria-modal="true"
      aria-label="What would you like to be called?"
      className="fixed inset-0 z-[70] flex items-center justify-center px-6"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      transition={{ duration: 0.35 }}
      style={overlayBg}
    >
      <motion.form
        onSubmit={(e) => {
          e.preventDefault();
          if (!pending) onSubmit(value);
        }}
        initial={{ opacity: 0, y: 16, scale: 0.98 }}
        animate={{ opacity: 1, y: 0, scale: 1 }}
        transition={{ duration: 0.4, ease: [0.22, 1, 0.36, 1] }}
        className="w-full max-w-sm rounded-3xl border border-slate-700 bg-deepsea p-6 sm:p-8"
        style={{ boxShadow: '0 24px 60px -20px rgba(0,0,0,0.5)' }}
      >
        <p className="font-mono text-xs uppercase tracking-widest text-mint">
          welcome
        </p>
        <h2 className="mt-2 font-display text-2xl font-bold text-white sm:text-3xl">
          What would you like to be called?
        </h2>
        <p className="mt-2 text-sm text-slate-400">
          Pick a name for around here. You can change it anytime on your
          account.
        </p>
        <input
          ref={inputRef}
          type="text"
          value={value}
          maxLength={40}
          onChange={(e) => setValue(e.target.value)}
          placeholder={defaultName || 'Your name'}
          aria-label="Your name"
          className="mt-5 w-full rounded-xl border border-slate-700 px-4 py-3 text-white outline-none transition focus:border-sky"
          style={{ background: 'var(--canvas)' }}
        />
        <div className="mt-5 flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={onSkip}
            disabled={pending}
            className="rounded-md px-4 py-2 text-sm font-semibold text-slate-300 transition hover:text-white disabled:opacity-60"
          >
            Skip
          </button>
          <button
            type="submit"
            disabled={pending}
            className="rounded-md bg-citron px-5 py-2 text-sm font-semibold text-ink transition hover:brightness-95 disabled:opacity-60"
          >
            {pending ? 'Saving…' : "That's me"}
          </button>
        </div>
      </motion.form>
    </motion.div>
  );
}

// ── Greeting ─────────────────────────────────────────────────────────────

// One animated word beat. Module-level (not defined during render) so its
// state isn't reset each frame. Under reduced motion it renders at final state.
function GreetWord({
  children,
  delay,
  duration,
  reduce,
  className,
}: {
  children: React.ReactNode;
  delay: number;
  duration: number;
  reduce: boolean | null;
  className?: string;
}) {
  return (
    <motion.span
      className={className}
      initial={reduce ? false : { opacity: 0, y: 18, filter: 'blur(6px)' }}
      animate={{ opacity: 1, y: 0, filter: 'blur(0px)' }}
      transition={
        reduce
          ? { duration: 0 }
          : { delay, duration, ease: [0.22, 1, 0.36, 1] }
      }
    >
      {children}
    </motion.span>
  );
}

function Greeting({
  name,
  isFriend,
  full,
  onDone,
}: {
  name: string;
  isFriend: boolean;
  full: boolean;
  onDone: () => void;
}) {
  const reduce = useReducedMotion();

  // Fire onDone exactly once (auto-timer, click, or Escape may race). Keep the
  // latest onDone in a ref (synced in an effect, never during render) so
  // `finish` stays stable and the auto-dismiss timer isn't reset each render.
  const doneRef = useRef(false);
  const onDoneRef = useRef(onDone);
  useEffect(() => {
    onDoneRef.current = onDone;
  });
  const finish = useCallback(() => {
    if (doneRef.current) return;
    doneRef.current = true;
    onDoneRef.current();
  }, []);

  // Per-word delays (seconds). FULL is a generous stagger with the friend line
  // arriving after the "hi {name}" line; SHORT lands everything almost at once.
  const t = full
    ? { hi: 0.15, name: 0.75, line2a: 1.7, line2b: 2.45, hold: 1.7 }
    : { hi: 0.05, name: 0.28, line2a: 0.5, line2b: 0.72, hold: 1.3 };
  const lastBeat = isFriend ? t.line2b : name ? t.name : t.hi;
  const wordDur = 0.55;

  // Auto-dismiss once the sequence has settled.
  useEffect(() => {
    const ms = reduce ? 2200 : (lastBeat + wordDur + t.hold) * 1000;
    const timer = setTimeout(finish, ms);
    return () => clearTimeout(timer);
  }, [reduce, lastBeat, t.hold, finish]);

  // Escape skips.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') finish();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [finish]);

  return (
    <motion.div
      role="dialog"
      aria-label="Welcome"
      onClick={finish}
      className="fixed inset-0 z-[70] flex flex-col items-center justify-center overflow-hidden px-6 text-center"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      transition={{ duration: 0.4 }}
      style={overlayBg}
    >
      {/* subtle drifting blob for warmth (off under reduced motion) */}
      {!reduce ? (
        <motion.div
          aria-hidden
          className="pointer-events-none absolute h-72 w-72 rounded-full"
          style={{
            background: 'color-mix(in srgb, var(--color-coral) 28%, transparent)',
            filter: 'blur(64px)',
          }}
          initial={{ x: -90, y: -50, opacity: 0 }}
          animate={{ x: [-90, 70, -90], y: [-50, 40, -50], opacity: 0.55 }}
          transition={{ duration: 13, repeat: Infinity, ease: 'easeInOut' }}
        />
      ) : null}

      <div className="relative flex flex-col items-center gap-4">
        <p className="flex flex-wrap items-baseline justify-center gap-x-4 font-display text-5xl font-bold leading-none text-white sm:text-7xl">
          <GreetWord delay={t.hi} duration={wordDur} reduce={reduce}>
            hi
          </GreetWord>
          {name ? (
            <GreetWord
              delay={t.name}
              duration={wordDur}
              reduce={reduce}
              className="text-citron"
            >
              {name}
            </GreetWord>
          ) : null}
        </p>
        {isFriend ? (
          <p className="flex flex-wrap items-baseline justify-center gap-x-3 font-display text-2xl font-medium leading-tight text-slate-300 sm:text-4xl">
            <GreetWord delay={t.line2a} duration={wordDur} reduce={reduce}>
              you are my
            </GreetWord>
            <GreetWord
              delay={t.line2b}
              duration={wordDur}
              reduce={reduce}
              className="font-bold text-mint"
            >
              friend
            </GreetWord>
          </p>
        ) : null}
      </div>

      <p className="absolute bottom-8 font-mono text-xs uppercase tracking-widest text-slate-400">
        tap to continue
      </p>
    </motion.div>
  );
}
