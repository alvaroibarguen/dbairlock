import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { StrictStyles, Toaster, TooltipProvider, useTheme } from '@/ui'

/** App-wide providers: CSP-safe styling, data cache, tooltip timing and the toast region. */
export function Providers({ client, children }: { client: QueryClient; children: ReactNode }) {
  const { resolved } = useTheme()
  return (
    <StrictStyles>
      <QueryClientProvider client={client}>
        <TooltipProvider>
          {children}
          <Toaster theme={resolved} />
        </TooltipProvider>
      </QueryClientProvider>
    </StrictStyles>
  )
}
