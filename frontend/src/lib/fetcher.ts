// Custom fetch mutator used by orval-generated hooks. Prepends the API base URL
// and unwraps JSON. The backend lives on a different origin (see ADR 0003), so
// the base URL is configured via NEXT_PUBLIC_API_URL.
//
// Lives outside src/lib/api/ because orval's `clean: true` wipes that folder.
const BASE_URL = process.env.NEXT_PUBLIC_API_URL ?? 'http://localhost:8080';

export const customFetch = async <T>(
  url: string,
  options?: RequestInit,
): Promise<T> => {
  const res = await fetch(`${BASE_URL}${url}`, {
    ...options,
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
