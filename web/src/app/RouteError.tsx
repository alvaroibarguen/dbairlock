import { isRouteErrorResponse, Link, useRouteError } from 'react-router'
import { isApiError } from '@/api'
import { Button, EmptyState, ErrorState, PageHeader, Pane, useDocumentTitle } from '@/ui'

/** Rendered when a route throws while loading or rendering. */
export function RouteError() {
  useDocumentTitle('Error')
  const error = useRouteError()
  let message = 'An unexpected error occurred.'
  let code: string | undefined
  if (isApiError(error)) {
    message = error.message
    code = error.code
  } else if (isRouteErrorResponse(error)) {
    message = `${error.status} ${error.statusText}`
  } else if (error instanceof Error) {
    message = error.message
  }
  return (
    <ErrorState
      className="min-h-dvh justify-center bg-panel"
      title="This page could not be shown."
      message={message}
      code={code}
      action={<Button onClick={() => window.location.reload()}>Reload the page</Button>}
    />
  )
}

export function NotFoundPage() {
  useDocumentTitle('Not found')
  return (
    <Pane className="h-full">
      <PageHeader as="h1" title="Not found" />
      <EmptyState
        className="flex-1 justify-center"
        title="There is no page at this address."
        action={
          <Button render={<Link to="/changes" />} nativeButton={false}>
            Go to changes
          </Button>
        }
      />
    </Pane>
  )
}
