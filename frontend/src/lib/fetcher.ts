// Custom fetch mutator used by orval-generated hooks. Prepends the API base URL
// and unwraps JSON. The backend lives on a different origin (see ADR 0003), so
// the base URL is configured via NEXT_PUBLIC_API_URL.
//
// Lives outside src/lib/api/ because orval's `clean: true` wipes that folder.
export const BASE_URL = process.env.NEXT_PUBLIC_API_URL ?? 'http://localhost:8080';

export const customFetch = async <T>(
  url: string,
  options?: RequestInit,
): Promise<T> => {
  const res = await fetch(`${BASE_URL}${url}`, {
    ...options,
    // Send the session cookie cross-origin (frontend and API are different
    // origins per ADR 0003; the opaque session cookie is scoped to
    // .aliflabs.dev per ADR 10). Without this the auth cookie is never
    // attached and every authenticated request reads as anonymous.
    credentials: 'include',
    headers: {
      'Content-Type': 'application/json',
      ...(options?.headers ?? {}),
    },
  });

  if (!res.ok) {
    const text = await res.text().catch(() => '');
    throw new Error(`API ${res.status}: ${text || res.statusText}`);
  }

  if (res.status === 204) {
    return undefined as T;
  }
  return (await res.json()) as T;
};

export default customFetch;
