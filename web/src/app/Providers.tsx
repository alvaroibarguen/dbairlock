import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { Toaster, TooltipProvider, useTheme } from '@/ui'

/** App-wide providers: data cache, tooltip timing and the toast region. */
export function Providers({ client, children }: { client: QueryClient; children: ReactNode }) {
  const { resolved } = useTheme()
  return (
    <QueryClientProvider client={client}>
      <TooltipProvider>
        {children}
        <Toaster theme={resolved} />
      </TooltipProvider>
    </QueryClientProvider>
  )
}
