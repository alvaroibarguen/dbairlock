import { readdirSync } from 'node:fs'
import js from '@eslint/js'
import { defineConfig, globalIgnores } from 'eslint/config'
import reactHooks from 'eslint-plugin-react-hooks'
import { reactRefresh } from 'eslint-plugin-react-refresh'
import globals from 'globals'
import tseslint from 'typescript-eslint'

/*
 * Module boundaries (phase 1, frontend):
 *   - ui/       design system: never imports features/, api/ or app/.
 *   - api/      typed client: never imports features/, app/ or ui/.
 *   - features/ one folder per feature: never imports another feature or app/.
 *   - app/      router, providers and shell: may import everything.
 * Cross-folder imports must use the `@/` alias so these rules can see them;
 * relative imports may not climb out of the current folder.
 */
const noParentImports = {
  regex: '^\\.\\./',
  message: 'Import across folders with the "@/" alias, not "../".',
}

const restrict = (...patterns) => ['error', { patterns: [noParentImports, ...patterns] }]

/*
 * No HTML injection (phase 11). SQL, titles, reasons, comments and anything
 * an agent writes are rendered as text only. These are the ways a string
 * becomes markup or code; React text nodes and attributes never do.
 * src/test/html-injection.test.ts checks each selector still fires.
 */
const noHtmlInjection = [
  'error',
  {
    selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']",
    message: 'Render text, never HTML: no dangerouslySetInnerHTML.',
  },
  {
    selector: "JSXAttribute[name.name='srcDoc']",
    message: 'Render text, never HTML: no iframe srcDoc.',
  },
  {
    selector: 'MemberExpression[property.name=/^(innerHTML|outerHTML)$/]',
    message: 'Render text, never HTML: use textContent or React, not innerHTML/outerHTML.',
  },
  {
    selector:
      'CallExpression[callee.property.name=/^(insertAdjacentHTML|createContextualFragment|setHTMLUnsafe|parseHTMLUnsafe)$/]',
    message: 'Render text, never HTML: no HTML parsing APIs.',
  },
  {
    selector: "CallExpression[callee.object.name='document'][callee.property.name=/^write(ln)?$/]",
    message: 'Render text, never HTML: no document.write.',
  },
  {
    selector: "NewExpression[callee.name='DOMParser']",
    message: 'Render text, never HTML: no DOMParser.',
  },
]

const featureNames = readdirSync(new URL('./src/features', import.meta.url), {
  withFileTypes: true,
})
  .filter((entry) => entry.isDirectory())
  .map((entry) => entry.name)

const featureBoundaries = featureNames.map((name) => ({
  files: [`src/features/${name}/**/*.{ts,tsx}`],
  rules: {
    'no-restricted-imports': restrict(
      {
        regex: `^@/features/(?!${name}(/|$))`,
        message: 'Features never import each other. Move shared code to ui/ or api/.',
      },
      { regex: '^@/app(/|$)', message: 'Features never import app/.' },
    ),
  },
}))

export default defineConfig([
  globalIgnores(['dist', 'coverage', 'playwright-report', 'test-results', 'src/api/schema.d.ts']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.strictTypeChecked,
      tseslint.configs.stylisticTypeChecked,
      reactHooks.configs.flat['recommended-latest'],
      reactRefresh.configs.vite(),
    ],
    languageOptions: {
      ecmaVersion: 2023,
      globals: globals.browser,
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
    },
    rules: {
      'no-restricted-imports': restrict(),
      'no-restricted-syntax': noHtmlInjection,
      // No eval in any form: the CSP forbids it too ('unsafe-eval' is absent).
      'no-eval': 'error',
      'no-new-func': 'error',
      'no-script-url': 'error',
      '@typescript-eslint/consistent-type-definitions': 'off',
      '@typescript-eslint/restrict-template-expressions': ['error', { allowNumber: true }],
      '@typescript-eslint/prefer-nullish-coalescing': [
        'error',
        { ignorePrimitives: { boolean: true } },
      ],
      '@typescript-eslint/no-confusing-void-expression': ['error', { ignoreArrowShorthand: true }],
    },
  },
  {
    files: ['src/ui/**/*.{ts,tsx}'],
    rules: {
      'no-restricted-imports': restrict({
        regex: '^@/(features|api|app)(/|$)',
        message: 'ui/ is the design system: it never imports features/, api/ or app/.',
      }),
    },
  },
  {
    files: ['src/api/**/*.{ts,tsx}'],
    rules: {
      'no-restricted-imports': restrict({
        regex: '^@/(features|app|ui)(/|$)',
        message: 'api/ is the typed client: it never imports features/, app/ or ui/.',
      }),
    },
  },
  ...featureBoundaries,
  {
    files: ['*.config.{js,ts}', 'e2e/**/*.ts'],
    languageOptions: { globals: globals.node },
    rules: { 'react-hooks/rules-of-hooks': 'off' },
  },
  {
    files: ['**/*.js'],
    extends: [js.configs.recommended],
    languageOptions: { globals: globals.node },
  },
])
