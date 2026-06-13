import { defineConfig } from 'orval';

// Generates React Query hooks + TS types from the backend's OpenAPI spec.
// The spec itself is generated from the Go handlers (see ADR 0005).
// Run with `npm run codegen` (also runs automatically before dev/build).
export default defineConfig({
  portfolio: {
    input: {
      target: '../backend/openapi.yaml',
    },
    output: {
      mode: 'split',
      target: './src/lib/api/generated.ts',
      schemas: './src/lib/api/model',
      client: 'react-query',
      httpClient: 'fetch',
      clean: true,
      override: {
        mutator: {
          path: './src/lib/fetcher.ts',
          name: 'customFetch',
        },
        // No global query.useQuery: in orval 8 that flag forces *every*
        // operation (including the POST /api/contact) into a query hook and
        // suppresses its mutation. Letting the HTTP verb decide keeps GETs as
        // useQuery and the contact POST as a useMutation (with .mutate).
        // Our custom fetcher returns the parsed body directly (not a
        // {data,status,headers} envelope), so hooks expose the body as `data`.
        fetch: {
          includeHttpResponseReturnType: false,
        },
      },
    },
  },
});
