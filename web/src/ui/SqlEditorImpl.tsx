import { sql } from '@codemirror/lang-sql'
import { syntaxHighlighting } from '@codemirror/language'
import { type Diagnostic, setDiagnostics } from '@codemirror/lint'
import { Prec } from '@codemirror/state'
import { EditorView, keymap } from '@codemirror/view'
import CodeMirror, { type ReactCodeMirrorRef } from '@uiw/react-codemirror'
import { useEffect, useMemo, useRef } from 'react'
import { cn } from './cn'
import type { SqlEditorProps } from './SqlEditor'
import { sqlConfig } from './sqlDialect'
import { editorHighlight } from './sqlHighlight'
import { byteToIndex } from './textOffset'

/** The editor's look, entirely from the design tokens, so both themes follow. */
const theme = EditorView.theme({
  '&': {
    backgroundColor: 'var(--bg)',
    color: 'var(--text)',
    fontSize: '13px',
    border: '1px solid var(--seam)',
    borderRadius: '6px',
  },
  '&.cm-focused': { outline: '2px solid var(--green)', outlineOffset: '0' },
  '.cm-scroller': {
    fontFamily: "'Geist Mono', ui-monospace, Menlo, monospace",
    lineHeight: '1.7',
    fontVariantNumeric: 'tabular-nums',
  },
  '.cm-content': { caretColor: 'var(--text)', padding: '8px 0' },
  '.cm-line': { padding: '0 12px 0 4px' },
  '.cm-gutters': {
    backgroundColor: 'var(--bg)',
    color: 'var(--mute)',
    border: 'none',
    borderTopLeftRadius: '6px',
    borderBottomLeftRadius: '6px',
  },
  '.cm-activeLine': { backgroundColor: 'color-mix(in oklab, var(--raise) 60%, transparent)' },
  '.cm-activeLineGutter': { backgroundColor: 'transparent', color: 'var(--dim)' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--text)' },
  '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground':
    { backgroundColor: 'color-mix(in oklab, var(--blue) 30%, transparent)' },
  '.cm-matchingBracket': {
    backgroundColor: 'color-mix(in oklab, var(--green) 18%, transparent)',
    outline: 'none',
  },
  '.cm-placeholder': { color: 'var(--mute)' },
  '.cm-tooltip': {
    backgroundColor: 'var(--raise)',
    border: '1px solid var(--seam)',
    borderRadius: '6px',
    color: 'var(--text)',
    boxShadow: 'var(--overlay-shadow)',
    overflow: 'hidden',
  },
  '.cm-tooltip-autocomplete > ul': { fontFamily: "'Geist Mono', ui-monospace, monospace" },
  '.cm-tooltip-autocomplete > ul > li': { padding: '2px 8px' },
  '.cm-tooltip-autocomplete > ul > li[aria-selected]': {
    backgroundColor: 'var(--seam)',
    color: 'var(--text)',
  },
  '.cm-completionDetail': { color: 'var(--mute)' },
  // The kind icons are symbol glyphs the mono font lacks.
  '.cm-completionIcon': { fontFamily: 'system-ui, sans-serif', color: 'var(--mute)' },
  '.cm-diagnostic': { padding: '4px 8px', fontFamily: "'Geist', system-ui, sans-serif" },
  '.cm-diagnostic-error': { borderLeft: '3px solid var(--red)' },
  '.cm-lintRange-error': {
    backgroundImage: 'none',
    textDecoration: 'underline wavy var(--red)',
    textUnderlineOffset: '3px',
  },
  '.cm-lint-marker-error': { content: 'none' },
})

/** The span a server error points at: from its offset to the end of the word. */
function diagnosticRange(text: string, position: number): { from: number; to: number } {
  if (position < 0 || text.length === 0) return { from: 0, to: text.length }
  const from = Math.min(byteToIndex(text, position), Math.max(text.length - 1, 0))
  const rest = /^[^\s,;()]+/.exec(text.slice(from))
  return { from, to: Math.min(text.length, from + Math.max(rest?.[0].length ?? 1, 1)) }
}

export default function SqlEditorImpl({
  value,
  onChange,
  dialect,
  schema,
  defaultNamespace,
  diagnostics,
  readOnly,
  onRun,
  placeholder,
  invalid,
  className,
  ...aria
}: SqlEditorProps) {
  const ref = useRef<ReactCodeMirrorRef>(null)
  const label = aria['aria-label']
  const describedBy = aria['aria-describedby']
  const extensions = useMemo(
    () => [
      sql(sqlConfig(dialect, schema, defaultNamespace)),
      syntaxHighlighting(editorHighlight),
      theme,
      EditorView.lineWrapping,
      EditorView.contentAttributes.of({
        'aria-label': label,
        ...(describedBy ? { 'aria-describedby': describedBy } : {}),
        ...(invalid ? { 'aria-invalid': 'true' } : {}),
      }),
      // Ctrl/Cmd+Enter belongs to the page (the wrapper runs onRun).
      Prec.highest(keymap.of([{ key: 'Mod-Enter', run: () => true }])),
    ],
    [dialect, schema, defaultNamespace, label, describedBy, invalid],
  )

  useEffect(() => {
    const view = ref.current?.view
    if (!view) return
    const text = view.state.doc.toString()
    const list: Diagnostic[] = (diagnostics ?? []).map((d) => ({
      ...diagnosticRange(text, d.position),
      severity: 'error',
      message: d.message,
    }))
    view.dispatch(setDiagnostics(view.state, list))
  }, [diagnostics])

  return (
    <div
      className="min-w-0"
      onKeyDownCapture={(e) => {
        if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && onRun) {
          e.preventDefault()
          e.stopPropagation()
          onRun()
        }
      }}
    >
      <CodeMirror
        ref={ref}
        value={value}
        onChange={(next) => onChange?.(next)}
        extensions={extensions}
        theme="none"
        editable={!readOnly}
        readOnly={readOnly}
        indentWithTab={false}
        placeholder={placeholder}
        minHeight="88px"
        basicSetup={{
          lineNumbers: true,
          foldGutter: false,
          highlightActiveLine: !readOnly,
          highlightActiveLineGutter: !readOnly,
          autocompletion: !readOnly,
          closeBrackets: true,
          bracketMatching: true,
          searchKeymap: false,
          lintKeymap: true,
          tabSize: 2,
        }}
        className={cn('min-w-0', className)}
      />
    </div>
  )
}
