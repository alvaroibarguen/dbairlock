import { useSyncExternalStore } from 'react'

export type ThemePreference = 'system' | 'dark' | 'light'
export type ResolvedTheme = 'dark' | 'light'

export const THEME_STORAGE_KEY = 'dbairlock.theme'
const LIGHT_QUERY = '(prefers-color-scheme: light)'

const listeners = new Set<() => void>()
let preference: ThemePreference = readStoredPreference()

function isPreference(value: unknown): value is ThemePreference {
  return value === 'system' || value === 'dark' || value === 'light'
}

/** Reads the saved preference. Storage can be unavailable (private mode, blocked). */
export function readStoredPreference(): ThemePreference {
  try {
    const stored = window.localStorage.getItem(THEME_STORAGE_KEY)
    return isPreference(stored) ? stored : 'system'
  } catch {
    return 'system'
  }
}

function systemTheme(): ResolvedTheme {
  if (typeof window.matchMedia !== 'function') return 'dark'
  return window.matchMedia(LIGHT_QUERY).matches ? 'light' : 'dark'
}

export function resolveTheme(pref: ThemePreference): ResolvedTheme {
  return pref === 'system' ? systemTheme() : pref
}

/**
 * Writes the preference to <html data-theme>. "system" leaves the choice to
 * the prefers-color-scheme media query in tokens.css.
 */
export function applyTheme(pref: ThemePreference = preference): void {
  document.documentElement.dataset.theme = pref
}

export function getThemePreference(): ThemePreference {
  return preference
}

/** Sets, persists and applies the theme preference. */
export function setThemePreference(next: ThemePreference): void {
  preference = next
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, next)
  } catch {
    // Storage unavailable: the choice still applies for this session.
  }
  applyTheme(next)
  listeners.forEach((listener) => listener())
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  const media = typeof window.matchMedia === 'function' ? window.matchMedia(LIGHT_QUERY) : null
  media?.addEventListener('change', listener)
  return () => {
    listeners.delete(listener)
    media?.removeEventListener('change', listener)
  }
}

/** The current preference and the theme actually shown, kept in sync with the OS setting. */
export function useTheme(): {
  preference: ThemePreference
  resolved: ResolvedTheme
  setPreference: (next: ThemePreference) => void
} {
  const pref = useSyncExternalStore(subscribe, getThemePreference, () => 'system' as const)
  const resolved = useSyncExternalStore(
    subscribe,
    () => resolveTheme(pref),
    () => 'dark' as const,
  )
  return { preference: pref, resolved, setPreference: setThemePreference }
}

/** Test helper: re-reads storage as if the page had just loaded. */
export function resetThemeForTests(): void {
  preference = readStoredPreference()
  applyTheme(preference)
}
