import { FormField, PageHeader, Pane, Select, type ThemePreference, useTheme } from '@/ui'

const themeOptions = [
  { value: 'system', label: 'System' },
  { value: 'dark', label: 'Dark' },
  { value: 'light', label: 'Light' },
] as const satisfies readonly { value: ThemePreference; label: string }[]

/** /settings. Instance settings arrive in phase 3; appearance works now. */
export function SettingsPage() {
  const { preference, setPreference } = useTheme()
  return (
    <Pane className="h-full">
      <PageHeader as="h1" title="Settings" />
      <div className="flex max-w-xl flex-col gap-6 px-6 py-6">
        <section aria-labelledby="appearance-title" className="flex flex-col gap-4">
          <h2 id="appearance-title" className="text-base font-medium small-caps text-dim">
            Appearance
          </h2>
          <FormField
            label="Theme"
            help="System follows your operating system. Saved in this browser only."
          >
            <Select
              className="w-60"
              options={themeOptions}
              value={preference}
              onValueChange={setPreference}
            />
          </FormField>
        </section>
      </div>
    </Pane>
  )
}
