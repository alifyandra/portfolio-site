'use client';

import { useQueryClient } from '@tanstack/react-query';

import {
  useUpdateCurrentUser,
  getGetCurrentUserQueryKey,
} from '@/lib/api/generated';
import { useAuth } from '@/lib/auth';
import { WA_COUNTRIES } from '@/lib/wa-countries';

const selectClass =
  'rounded-md border border-slate-700 bg-deepsea px-3 py-2 text-white outline-none focus:border-sky';

// The user's default country code applies to every list that doesn't set its
// own override (see ListsPanel). It's stored on the User (ADR 11) and read back
// through the auth context, so after a change we refetch /api/auth/me to keep
// the per-list hint and future sends in sync everywhere.
export function CountryCodeSetting() {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const update = useUpdateCurrentUser();

  const current = user?.default_country_code ?? '';
  const known = WA_COUNTRIES.some((c) => c.code === current);

  const onChange = (code: string) => {
    if (code === '' || code === current) return;
    update.mutate(
      { data: { default_country_code: code } },
      {
        onSuccess: () =>
          queryClient.invalidateQueries({
            queryKey: getGetCurrentUserQueryKey(),
          }),
      },
    );
  };

  return (
    <section className="flex flex-col gap-2">
      <label className="flex flex-col gap-1 text-sm text-slate-300">
        Default country code for local (0…) numbers
        <select
          className={selectClass}
          value={current}
          disabled={!user || update.isPending}
          onChange={(e) => onChange(e.target.value)}
        >
          {current === '' && <option value="">Choose a country…</option>}
          {/* Keep the stored value selectable even if it isn't in the curated
              list, so the control always reflects the real default. */}
          {!known && current !== '' && (
            <option value={current}>+{current}</option>
          )}
          {WA_COUNTRIES.map((c) => (
            <option key={c.code} value={c.code}>
              {c.name} (+{c.code})
            </option>
          ))}
        </select>
      </label>
      <p className="text-xs text-slate-400">
        Applied to numbers starting with 0 in every list that doesn’t set its
        own code below.
      </p>
      {update.error && (
        <p className="text-sm text-coral">{(update.error as Error).message}</p>
      )}
    </section>
  );
}
