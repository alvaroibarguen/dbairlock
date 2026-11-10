import { PanelRightClose, PanelRightOpen } from 'lucide-react'
import { type ReactNode, useState } from 'react'
import {
  Button,
  cn,
  MEDIUM,
  PageHeader,
  Pane,
  PaneGrid,
  Tabs,
  Tooltip,
  useMediaQuery,
  WIDE,
} from '@/ui'

export interface ChangesLayoutProps {
  queue: ReactNode
  queueActions?: ReactNode
  detail: ReactNode
  rows: ReactNode
  rowsActions?: ReactNode
  /** Page heading for assistive tech; the panes carry the visible labels. */
  title?: string
}

const ROWS_TITLE = 'Affected rows · before / after'

/**
 * The changes screen: queue (320px) · detail (fluid) · affected rows (548px,
 * collapsible). Below 1280px the rows pane becomes a tab inside the detail
 * pane; below 768px the queue and detail stack.
 */
export function ChangesLayout({
  queue,
  queueActions,
  detail,
  rows,
  rowsActions,
  title = 'Changes',
}: ChangesLayoutProps) {
  const wide = useMediaQuery(WIDE, true)
  const medium = useMediaQuery(MEDIUM, true)
  const [rowsOpen, setRowsOpen] = useState(true)

  const queuePane = (
    <Pane aria-labelledby="queue-title" className="min-h-0">
      <h1 className="sr-only">{title}</h1>
      <PageHeader title={<span id="queue-title">Review queue</span>} actions={queueActions} />
      <div className="min-h-0 flex-1 overflow-y-auto">{queue}</div>
    </Pane>
  )

  if (wide) {
    return (
      <PaneGrid
        className={cn(
          'h-full min-h-0 transition-[grid-template-columns] duration-160 ease-out',
          rowsOpen
            ? 'grid-cols-[320px_minmax(0,1fr)_548px]'
            : 'grid-cols-[320px_minmax(0,1fr)_44px]',
        )}
      >
        {queuePane}
        <Pane aria-label="Change detail" className="min-h-0 overflow-y-auto">
          {detail}
        </Pane>
        {rowsOpen ? (
          <Pane aria-labelledby="rows-title" className="min-h-0">
            <PageHeader
              title={<span id="rows-title">{ROWS_TITLE}</span>}
              actions={
                <>
                  {rowsActions}
                  <Tooltip content="Collapse affected rows" side="left">
                    <Button
                      variant="ghost"
                      size="icon"
                      className="-mr-2 size-8"
                      aria-label="Collapse affected rows"
                      aria-expanded="true"
                      onClick={() => setRowsOpen(false)}
                      icon={<PanelRightClose aria-hidden="true" className="size-4" />}
                    />
                  </Tooltip>
                </>
              }
            />
            <div className="min-h-0 flex-1 overflow-auto">{rows}</div>
          </Pane>
        ) : (
          <Pane aria-label="Affected rows (collapsed)" className="items-center pt-1.5">
            <Tooltip content="Expand affected rows" side="left">
              <Button
                variant="ghost"
                size="icon"
                className="size-8"
                aria-label="Expand affected rows"
                aria-expanded="false"
                onClick={() => setRowsOpen(true)}
                icon={<PanelRightOpen aria-hidden="true" className="size-4" />}
              />
            </Tooltip>
          </Pane>
        )}
      </PaneGrid>
    )
  }

  const detailWithRows = (
    <Pane aria-label="Change detail" className="min-h-0">
      <Tabs
        aria-label="Change detail"
        className="min-h-0 flex-1"
        items={[
          { value: 'detail', label: 'Detail', content: detail },
          { value: 'rows', label: 'Affected rows', content: rows },
        ]}
      />
    </Pane>
  )

  if (medium) {
    return (
      <PaneGrid className="h-full min-h-0 grid-cols-[320px_minmax(0,1fr)]">
        {queuePane}
        {detailWithRows}
      </PaneGrid>
    )
  }

  return (
    <PaneGrid className="flex-1 grid-cols-1 grid-rows-[auto_1fr]">
      {queuePane}
      {detailWithRows}
    </PaneGrid>
  )
}
