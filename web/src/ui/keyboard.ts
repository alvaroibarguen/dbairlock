/**
 * Whether a global single-key or Ctrl/Cmd shortcut should stand aside: focus
 * is in a text field or editor (the key belongs to it), or a dialog is open.
 */
export function typing(): boolean {
  const el = document.activeElement
  return (
    el instanceof HTMLInputElement ||
    el instanceof HTMLTextAreaElement ||
    el instanceof HTMLSelectElement ||
    (el instanceof HTMLElement && el.isContentEditable) ||
    document.querySelector('[role="dialog"]') !== null
  )
}
