import { HighlightStyle } from '@codemirror/language'
import { highlightCode, tags, tagHighlighter } from '@lezer/highlight'
import { sqlDialect } from './sqlDialect'

/*
 * SQL syntax colours (DESIGN.md, SQL block): keywords blue, strings green,
 * numbers amber, functions violet, operators mute. Imported only by lazily
 * loaded modules, so CodeMirror stays out of the initial bundle.
 */

/** Token classes for read-only SQL, as Tailwind utility classes. */
const classes = tagHighlighter([
  { tag: [tags.keyword, tags.bool, tags.null, tags.typeName], class: 'text-blue' },
  { tag: [tags.string, tags.special(tags.string)], class: 'text-green' },
  { tag: tags.number, class: 'text-amber' },
  { tag: tags.standard(tags.name), class: 'text-violet' },
  { tag: [tags.operator, tags.punctuation], class: 'text-mute' },
  { tag: [tags.lineComment, tags.blockComment], class: 'text-mute italic' },
])

/** The same colours for the editor, through the design tokens. */
export const editorHighlight = HighlightStyle.define([
  { tag: [tags.keyword, tags.bool, tags.null, tags.typeName], color: 'var(--blue)' },
  { tag: [tags.string, tags.special(tags.string)], color: 'var(--green)' },
  { tag: tags.number, color: 'var(--amber)' },
  { tag: tags.standard(tags.name), color: 'var(--violet)' },
  { tag: [tags.operator, tags.punctuation], color: 'var(--mute)' },
  { tag: [tags.lineComment, tags.blockComment], color: 'var(--mute)', fontStyle: 'italic' },
])

/** One run of text and the class it is shown with ("" for plain). */
export interface SqlSegment {
  text: string
  className: string
}

/**
 * Splits SQL into coloured runs. The text is never interpreted: runs are
 * rendered as React text. An identifier followed by "(" is a function call
 * and coloured like one.
 */
export function highlightSql(text: string, dialect: string): SqlSegment[][] {
  const lang = sqlDialect(dialect).language
  const tree = lang.parser.parse(text)
  const lines: SqlSegment[][] = [[]]
  highlightCode(
    text,
    tree,
    classes,
    (chunk, className) => lines[lines.length - 1]?.push({ text: chunk, className }),
    () => lines.push([]),
  )
  return lines.map((line) => line.flatMap((seg, i) => markCalls(seg, line[i - 1], line[i + 1])))
}

const callPattern = /[A-Za-z_][A-Za-z0-9_.]*(?=\s*\()/g

/** Keywords that are followed by "(" without being calls. */
const structural = new Set([
  'in',
  'values',
  'exists',
  'any',
  'all',
  'some',
  'not',
  'and',
  'or',
  'on',
  'using',
  'over',
  'filter',
  'within',
  'as',
  'into',
  'set',
  'where',
  'select',
  'from',
  'case',
  'when',
  'then',
  'else',
  'returning',
  'table',
  'row',
  'array',
  'is',
  'like',
  'between',
  'join',
  'with',
])

/**
 * Splits plain text so names followed by "(" are coloured as calls. A table
 * name after INTO is followed by its column list, not called.
 */
function markCalls(
  seg: SqlSegment,
  prev: SqlSegment | undefined,
  next: SqlSegment | undefined,
): SqlSegment[] {
  if (seg.className === 'text-blue') {
    // Dialects list some functions (lower, coalesce) as keywords.
    const word = seg.text.trim().toLowerCase()
    const called = /^[a-z_]+$/.test(word) && next?.text.trimStart().startsWith('(')
    return [called && !structural.has(word) ? { ...seg, className: 'text-violet' } : seg]
  }
  if (seg.className !== '') return [seg]
  const afterInto = prev !== undefined && /\binto\s*$/i.test(prev.text)
  const out: SqlSegment[] = []
  let last = 0
  for (const m of seg.text.matchAll(callPattern)) {
    if (afterInto && seg.text.slice(0, m.index).trim() === '') continue
    if (m.index > last) out.push({ text: seg.text.slice(last, m.index), className: '' })
    out.push({ text: m[0], className: 'text-violet' })
    last = m.index + m[0].length
  }
  const rest = seg.text.slice(last)
  if (rest) {
    // A name at the end of this run, called in the next one.
    const tail = /[A-Za-z_][A-Za-z0-9_.]*$/.exec(rest)
    const isTable = afterInto && last === 0 && rest.slice(0, tail?.index ?? 0).trim() === ''
    if (tail && !isTable && next?.text.trimStart().startsWith('(')) {
      if (tail.index > 0) out.push({ text: rest.slice(0, tail.index), className: '' })
      out.push({ text: tail[0], className: 'text-violet' })
    } else {
      out.push({ text: rest, className: '' })
    }
  }
  return out
}
