import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MenuRadioGroup, Menu } from './Menu'
import {
  readStoredPreference,
  resetThemeForTests,
  setThemePreference,
  THEME_STORAGE_KEY,
  useTheme,
} from './theme'

function ThemeMenu() {
  const { preference, setPreference } = useTheme()
  return (
    <Menu trigger={<button type="button">Open</button>}>
      <MenuRadioGroup
        label="Theme"
        value={preference}
        onValueChange={setPreference}
        options={[
          { value: 'system', label: 'System' },
          { value: 'dark', label: 'Dark' },
          { value: 'light', label: 'Light' },
        ]}
      />
    </Menu>
  )
}

describe('theme', () => {
  beforeEach(() => {
    localStorage.clear()
    resetThemeForTests()
  })
  afterEach(() => {
    localStorage.clear()
  })

  it('follows the system by default', () => {
    expect(readStoredPreference()).toBe('system')
    expect(document.documentElement.dataset.theme).toBe('system')
  })

  it('switches, persists and applies the choice from the menu', async () => {
    render(<ThemeMenu />)
    await userEvent.click(screen.getByRole('button', { name: 'Open' }))
    await userEvent.click(await screen.findByRole('menuitemradio', { name: 'Light' }))

    expect(document.documentElement.dataset.theme).toBe('light')
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe('light')

    await userEvent.click(screen.getByRole('button', { name: 'Open' }))
    expect(await screen.findByRole('menuitemradio', { name: 'Light' })).toHaveAttribute(
      'aria-checked',
      'true',
    )
  })

  it('restores the saved choice on load and ignores junk values', () => {
    localStorage.setItem(THEME_STORAGE_KEY, 'dark')
    resetThemeForTests()
    expect(document.documentElement.dataset.theme).toBe('dark')

    localStorage.setItem(THEME_STORAGE_KEY, 'purple')
    expect(readStoredPreference()).toBe('system')
  })

  it('still applies the theme when storage is unavailable', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError')
    })
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError')
    })
    expect(() => {
      setThemePreference('light')
    }).not.toThrow()
    expect(document.documentElement.dataset.theme).toBe('light')
    expect(readStoredPreference()).toBe('system')
  })
})
