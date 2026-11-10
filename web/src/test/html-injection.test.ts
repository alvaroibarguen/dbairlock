// @vitest-environment node
import { fileURLToPath } from 'node:url'
import { ESLint } from 'eslint'
import { describe, expect, it } from 'vitest'

const root = fileURLToPath(new URL('../..', import.meta.url))
const eslint = new ESLint({ cwd: root })

/** Lints `code` as if it were an existing feature file, returning HTML-injection and eval errors. */
async function injectionErrors(code: string): Promise<string[]> {
  const [result] = await eslint.lintText(code, {
    filePath: `${root}/src/features/changes/ChangesPage.tsx`,
  })
  return (result?.messages ?? [])
    .filter((m) => ['no-restricted-syntax', 'no-eval', 'no-new-func'].includes(m.ruleId ?? ''))
    .map((m) => m.message)
}

// The first type-aware lint builds the TypeScript program, which takes
// seconds on a loaded machine.
describe('no HTML injection', { timeout: 60_000 }, () => {
  it.each([
    [
      'dangerouslySetInnerHTML',
      'export const X = (p: { s: string }) => <div dangerouslySetInnerHTML={{ __html: p.s }} />\n',
    ],
    ['srcDoc', 'export const X = (p: { s: string }) => <iframe title="x" srcDoc={p.s} />\n'],
    ['innerHTML', 'export function f(el: HTMLElement, s: string) {\n  el.innerHTML = s\n}\n'],
    ['outerHTML', 'export function f(el: HTMLElement, s: string) {\n  el.outerHTML = s\n}\n'],
    [
      'insertAdjacentHTML',
      "export function f(el: HTMLElement, s: string) {\n  el.insertAdjacentHTML('beforeend', s)\n}\n",
    ],
    [
      'createContextualFragment',
      'export function f(r: Range, s: string) {\n  return r.createContextualFragment(s)\n}\n',
    ],
    ['document.write', 'export function f(s: string) {\n  document.write(s)\n}\n'],
    ['DOMParser', "export const d = new DOMParser().parseFromString('', 'text/html')\n"],
    ['eval', 'export function f(s: string): unknown {\n  return eval(s)\n}\n'],
    ['new Function', "export const g = new Function('return 1')\n"],
  ])('rejects %s', async (_, code) => {
    expect(await injectionErrors(code)).not.toHaveLength(0)
  })

  it('allows rendering text', async () => {
    expect(
      await injectionErrors(
        'export const X = (p: { s: string }) => <pre>{p.s}</pre>\nexport function f(el: HTMLElement, s: string) {\n  el.textContent = s\n}\n',
      ),
    ).toEqual([])
  })
})
