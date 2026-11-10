import { CircleAlert, CircleCheck, Info, TriangleAlert } from 'lucide-react'
import { Toaster as Sonner } from 'sonner'
import { Spinner } from './Spinner'

/**
 * The toast region, styled with the Graphite Console tokens. Mount once.
 * Call `toast()`, `toast.success()`, `toast.error()` from anywhere.
 */
export function Toaster({ theme = 'system' }: { theme?: 'light' | 'dark' | 'system' }) {
  return (
    <Sonner
      theme={theme}
      position="bottom-right"
      closeButton
      containerAriaLabel="Notifications"
      icons={{
        success: <CircleCheck aria-hidden="true" className="size-4 text-green" />,
        error: <CircleAlert aria-hidden="true" className="size-4 text-red" />,
        warning: <TriangleAlert aria-hidden="true" className="size-4 text-amber" />,
        info: <Info aria-hidden="true" className="size-4 text-blue" />,
        loading: <Spinner className="text-mute" />,
      }}
      toastOptions={{
        unstyled: true,
        classNames: {
          toast:
            'group flex w-(--width) items-start gap-2.5 rounded-md border border-seam bg-raise py-3 pr-9 pl-3.5 font-sans text-base text-text shadow-overlay',
          icon: 'mt-px flex shrink-0',
          content: 'flex min-w-0 flex-col gap-0.5',
          title: 'font-medium',
          description: 'text-dim',
          error: 'border-red/50',
          warning: 'border-amber/50',
          closeButton:
            'absolute! top-2.5! right-2! left-auto! flex! size-6! cursor-pointer items-center justify-center rounded-lg! border-0! bg-transparent! text-mute! transform-none! hover:bg-seam! hover:text-text!',
          actionButton:
            'ml-auto h-7 shrink-0 cursor-pointer rounded-md border border-seam bg-panel px-2.5 text-xs font-medium text-text hover:border-mute/60',
        },
      }}
    />
  )
}
