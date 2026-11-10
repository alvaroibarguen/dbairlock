/** Floating overlays (menus, popups, dialogs, toasts): --raise surface, seam border, one soft shadow. */
export const overlaySurface = 'rounded-md border border-seam bg-raise text-text shadow-overlay'

/** Enter/exit transition for positioned popups, driven by Base UI data attributes. */
export const popupMotion =
  'origin-(--transform-origin) transition-[scale,opacity] duration-120 ease-out ' +
  'data-starting-style:scale-[0.98] data-starting-style:opacity-0 ' +
  'data-ending-style:scale-[0.98] data-ending-style:opacity-0'

/** A selectable row inside a menu or select list. */
export const listItem =
  'flex min-h-8 cursor-default items-center gap-2 rounded-[4px] px-2 text-base text-text outline-none select-none ' +
  'data-highlighted:bg-seam data-disabled:opacity-50'
