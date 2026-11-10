import { twMerge } from 'tailwind-merge'

type ClassValue = string | false | null | undefined | 0

/** Joins class names and resolves Tailwind conflicts (later classes win). */
export function cn(...classes: ClassValue[]): string {
  return twMerge(classes.filter(Boolean).join(' '))
}
