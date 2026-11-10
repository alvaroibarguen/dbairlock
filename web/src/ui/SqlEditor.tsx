import { lazy, Suspense } from 'react'
import { cn } from './cn'
import type { CompletionSchema } from './sqlDialect'

/** A problem to underline in the editor. */
export interface SqlDiagnostic {
  /** Byte offset into the statement (as the server reports it), or -1 for the whole text. */
  position: number
  message: string
}

export interface SqlEditorProps {
  value: string
  onChange?: (value: string) => void
  /** The connector's editor dialect, for example "postgresql". */
  dialect: string
  /** Tables and columns for completion. */
  schema?: CompletionSchema
  /** The namespace unqualified names complete from. */
  defaultNamespace?: string
  diagnostics?: readonly SqlDiagnostic[]
  readOnly?: boolean
  /** Ctrl/Cmd+Enter. */
  onRun?: () => void
  'aria-label': string
  /** Id of an element describing the editor, for example its error. */
  'aria-describedby'?: string
  placeholder?: string
  invalid?: boolean
  className?: string
}

const SqlEditorImpl = lazy(() => import('./SqlEditorImpl'))

/**
 * The SQL editor: CodeMirror 6 with the connector's dialect, schema
 * completion, inline diagnostics and the design tokens. CodeMirror loads
 * lazily, so it is not in the initial bundle; until then a plain block holds
 * the text.
 */
export function SqlEditor(props: SqlEditorProps) {
  return (
    <Suspense
      fallback={
        <pre
          aria-busy="true"
          className={cn(
            'min-h-[88px] rounded-md border border-seam bg-bg px-3 py-2 font-mono text-base leading-[1.7] whitespace-pre-wrap text-dim',
            props.className,
          )}
        >
          {props.value || props.placeholder}
        </pre>
      }
    >
      <SqlEditorImpl {...props} />
    </Suspense>
  )
}
