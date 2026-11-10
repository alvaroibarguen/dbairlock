/** Shared look for text controls: 32px, --bg fill, seam border, green focus ring, red when invalid. */
export const controlClass =
  'w-full min-w-0 rounded-md border border-seam bg-bg px-2.5 text-base text-text placeholder:text-mute ' +
  'transition-[border-color,outline-color] duration-120 ease-out ' +
  'hover:not-data-disabled:border-mute/60 ' +
  'focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-green focus:outline-2 focus:outline-offset-0 focus:outline-green ' +
  'data-invalid:border-red aria-invalid:border-red data-invalid:focus:outline-red aria-invalid:focus:outline-red ' +
  'data-disabled:cursor-not-allowed data-disabled:opacity-50 disabled:cursor-not-allowed disabled:opacity-50'
