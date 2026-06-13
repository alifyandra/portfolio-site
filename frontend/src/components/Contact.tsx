'use client';

import { useState } from 'react';
import { Section } from './Section';
import { useCreateContactMessage } from '@/lib/api/generated';
import { profile } from '@/lib/resume';

export function Contact() {
  const [form, setForm] = useState({ name: '', email: '', body: '', website: '' });
  const [done, setDone] = useState(false);
  const mutation = useCreateContactMessage();

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    mutation.mutate(
      { data: form },
      { onSuccess: () => setDone(true) },
    );
  };

  return (
    <Section id="contact" title="Get in Touch">
      {done ? (
        <p className="text-mint">
          Thanks, got your message. I&apos;ll get back to you at{' '}
          {form.email}.
        </p>
      ) : (
        <form onSubmit={onSubmit} className="flex max-w-xl flex-col gap-4">
          {/* Honeypot: hidden from users; bots that fill it get silently dropped. */}
          <input
            type="text"
            name="website"
            tabIndex={-1}
            autoComplete="off"
            aria-hidden="true"
            className="hidden"
            value={form.website}
            onChange={(e) => setForm({ ...form, website: e.target.value })}
          />
          <input
            required
            placeholder="Your name"
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
            className="rounded-md border border-slate-700 bg-deepsea px-3 py-2 text-white outline-none focus:border-sky"
          />
          <input
            required
            type="email"
            placeholder="Your email"
            value={form.email}
            onChange={(e) => setForm({ ...form, email: e.target.value })}
            className="rounded-md border border-slate-700 bg-deepsea px-3 py-2 text-white outline-none focus:border-sky"
          />
          <textarea
            required
            rows={5}
            placeholder="Your message"
            value={form.body}
            onChange={(e) => setForm({ ...form, body: e.target.value })}
            className="rounded-md border border-slate-700 bg-deepsea px-3 py-2 text-white outline-none focus:border-sky"
          />
          {mutation.isError && (
            <p className="text-sm text-coral">
              Something went wrong sending your message. Try again or email{' '}
              {profile.email}.
            </p>
          )}
          <button
            type="submit"
            disabled={mutation.isPending}
            className="self-start rounded-md bg-citron px-5 py-2.5 font-semibold text-deepsea transition hover:brightness-95 disabled:opacity-60"
          >
            {mutation.isPending ? 'Sending…' : 'Send message'}
          </button>
        </form>
      )}
    </Section>
  );
}
