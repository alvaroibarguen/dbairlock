// @vitest-environment node
import { fileURLToPath } from 'node:url'
import { ESLint } from 'eslint'
import { describe, expect, it } from 'vitest'

const root = fileURLToPath(new URL('../..', import.meta.url))
const eslint = new ESLint({ cwd: root })

/** Lints `code` as if it were the contents of an existing file, returning boundary errors. */
async function boundaryErrors(filePath: string, code: string): Promise<string[]> {
  const [result] = await eslint.lintText(code, { filePath: `${root}/${filePath}` })
  return (result?.messages ?? [])
    .filter((m) => m.ruleId === 'no-restricted-imports')
    .map((m) => m.message)
}

describe('module boundaries', () => {
  it('forbids ui/ from importing features/ or api/', async () => {
    const errors = await boundaryErrors(
      'src/ui/Button.tsx',
      "import { ChangesPage } from '@/features/changes/ChangesPage'\nimport { api } from '@/api'\nexport const x = [ChangesPage, api]\n",
    )
    expect(errors).toHaveLength(2)
    expect(errors[0]).toMatch(/ui\/ is the design system/)
  })

  it('forbids one feature from importing another', async () => {
    const errors = await boundaryErrors(
      'src/features/changes/ChangesPage.tsx',
      "import { AuditPage } from '@/features/audit/AuditPage'\nexport const x = AuditPage\n",
    )
    expect(errors).toEqual([expect.stringMatching(/Features never import each other/)])
  })

  it('allows a feature to import itself, ui/ and api/', async () => {
    const errors = await boundaryErrors(
      'src/features/changes/ChangesPage.tsx',
      "import { ChangesLayout } from '@/features/changes/ChangesLayout'\nimport { Button } from '@/ui'\nimport { api } from '@/api'\nexport const x = [ChangesLayout, Button, api]\n",
    )
    expect(errors).toEqual([])
  })

  it('forbids relative imports that climb out of a folder', async () => {
    const errors = await boundaryErrors(
      'src/ui/Button.tsx',
      "import { ChangesPage } from '../features/changes/ChangesPage'\nexport const x = ChangesPage\n",
    )
    expect(errors).toEqual([expect.stringMatching(/"@\/" alias/)])
  })
})
