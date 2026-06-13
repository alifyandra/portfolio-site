// Next 16 removed the `next lint` command, so we run ESLint directly. As of
// eslint-config-next 16 the shared config is published as a native flat-config
// array, so we spread it straight in — no FlatCompat / @eslint/eslintrc bridge.
//
// ESLint is intentionally held at v9: eslint-config-next 16 still bundles
// eslint-plugin-react 7.37.5 (its latest), which calls context.getFilename()
// — an API ESLint 10 removed, so linting crashes under v10. Bump to ESLint 10
// once eslint-plugin-react ships a release that drops getFilename().
import nextCoreWebVitals from 'eslint-config-next/core-web-vitals';

const eslintConfig = [
  ...nextCoreWebVitals,
  {
    // orval-generated React Query hooks + models (gitignored, regenerated).
    ignores: ['src/lib/api/generated.ts', 'src/lib/api/model/**'],
  },
];

export default eslintConfig;
