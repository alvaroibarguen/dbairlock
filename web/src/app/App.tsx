import { useState } from 'react'
import { RouterProvider } from 'react-router/dom'
import { Providers } from './Providers'
import { createQueryClient } from './queryClient'
import { createAppRouter } from './router'

const router = createAppRouter()

export function App() {
  const [client] = useState(createQueryClient)
  return (
    <Providers client={client}>
      <RouterProvider router={router} />
    </Providers>
  )
}
