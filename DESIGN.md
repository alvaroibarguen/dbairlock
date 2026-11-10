# Design: Graphite Console

The dbairlock dashboard is a working tool for backend engineers. The owner chose the **Graphite Console** direction on 2026-11-09 from four options. The static reference for the change review screen is [docs/design/review-screen-reference.html](docs/design/review-screen-reference.html). Every screen follows this file.

## Thesis

Graphite panels, hairline seams, and colour only where state lives. The UI reads instantly as a developer tool, and its quality shows in precision: aligned grids, tabular figures, honest states, keyboard-first flow.

## Layout

- **Icon rail** (52 px, darkest ground) on the far left: logo, Changes, New change, Audit log, Connections, then Settings and the user avatar at the bottom. Every icon has a tooltip and an `aria-label`.
- **Persistent panes** tile the rest of the viewport and are separated by 1 px seams (the gap between panes shows the seam colour). No drop shadows anywhere except floating overlays (menus, dialogs, toasts).
- The changes screen is three panes: **queue list** (320 px) · **change detail** (fluid) · **affected rows** (~548 px, collapsible). Navigation changes pane contents, not the layout.
- Other screens (Audit log, Connections, Settings) use a list pane plus a detail pane, or one full-width pane with a 44 px pane header.
- Responsive: panes collapse by priority. Below 1280 px the rows pane becomes a tab inside the detail pane; below 768 px the queue and detail stack and the rail becomes a top bar with a menu button. Usable by keyboard alone at 375 px.

## Colour tokens

Dark is the default theme; light is a full first-class theme. The system setting decides, with a manual override (System / Dark / Light) in the user menu. All colours are CSS variables in `web/src/ui/tokens.css`, exposed to Tailwind through `@theme`.

| Token | Dark | Light | Use |
|---|---|---|---|
| `--bg` | `#16181c` | `#f4f5f7` | rail, code blocks, recessed areas |
| `--panel` | `#22252b` | `#ffffff` | pane surfaces |
| `--raise` | `#2a2e35` | `#eef0f3` | selected row, secondary buttons, chips |
| `--seam` | `#30343b` | `#dfe2e7` | 1 px seams and borders |
| `--text` | `#e6e8eb` | `#16181c` | primary text |
| `--dim` | `#a3a9b2` | `#4b5260` | secondary text |
| `--mute` | `#8a919b` | `#6b7280` | labels, metadata (must still pass 4.5:1 on panel) |
| `--green` | `#7ee0a1` | `#17803d` | fresh, approved, executed, primary action, "after" values |
| `--amber` | `#f2a65a` | `#b45309` | pending review, warnings |
| `--red` | `#ff7b97` | `#c81e45` | destructive actions, failed, rejected, "before" values |
| `--blue` | `#8ab4ff` | `#2457c5` | SQL keywords, links, info |
| `--violet` | `#c3a6ff` | `#7a3fd1` | **agents only**: the AGENT badge and agent-authored markers; SQL functions |

The primary button is green with dark text (`#0f1a13`) in dark theme; in light theme it is `--green` with white text. Status colour never appears as decoration. Agent violet is never used for anything except agents and SQL function names.

## Type

- UI: **Geist** (400, 500, 600), 13 px base, line-height 1.45. Page titles 20 px / 600 / -0.01em.
- Mono: **Geist Mono** (400, 500) for SQL, ids (`CHG-0142`), hashes, timestamps, counts and every table cell holding data. Always `font-variant-numeric: tabular-nums`.
- Pane headers and section labels: 13 px, `font-variant: all-small-caps`, letter-spacing 0.06em, colour `--dim`.
- Self-host the fonts from the `geist` npm package (no Google Fonts request at runtime; dbairlock runs inside private networks).

## Spacing, radii, density

- 4 px base grid. Pane padding 16 px (lists) / 24 px (detail). Queue rows ~56 px, table rows 32 px.
- Radii: 6 px for buttons, inputs, code blocks and cards; 5 px for chips and tags; 8 px for icon buttons; 50% for avatars and status dots.
- Dense by default; never hide data behind hover.

## Components

- **Buttons**: 32 px high. Primary = solid green. Secondary = `--raise` fill with seam border. Destructive = transparent with `--red` text and a 50%-alpha red border, always placed **apart** from the primary action (pushed to the far end of the action bar) with a short consequence hint beside it. Keyboard hints in mono at 60% opacity (`⌘↵`).
- **Tags/pills**: 5 px radius, tinted fill at 8% alpha with a 35%-alpha border in the state colour.
- **Status dots**: 7 px circles, colour by status (amber pending, green executed/approved, red rejected/failed, mute for expired/cancelled/draft).
- **AGENT badge**: violet mono uppercase `AGENT` label, then the agent name and "via <owner> token". A human author is plain text with an avatar. Authorship must be unmistakable.
- **SQL block**: `--bg` fill, seam border, Geist Mono 13 px / 1.7, syntax colours: keywords blue, strings green, numbers amber, functions violet, operators mute.
- **Before/after table**: rows paired (before, then after) per primary key. Before values that change: red with strikethrough. After values that change: green with 8% green fill and a 2 px inset green left edge. Unchanged values: `--dim`.
- **Preview freshness**: the age counts up live in mono (`2:46 / fresh until 5:00`) with a 3 px progress bar; it turns amber in the last minute and the Approve button is disabled with an explanation once stale.
- **Inputs**: 32 px, `--bg` fill, seam border, 2 px green focus ring (`outline`), error state red border plus message below.
- **Focus**: every interactive element shows a visible 2 px focus ring; never remove outlines without a replacement.
- **Overlays** (menus, dialogs, drawers, toasts): `--raise` surface, seam border, one soft shadow (`0 8px 24px rgb(0 0 0 / 0.35)` dark, `0 8px 24px rgb(16 24 40 / 0.12)` light).
- **Empty and error states**: one sentence that says what is true and what to do next, plus one action. No illustrations.

## Motion

Minimal and fast: 120 ms ease-out for hover/press, 160 ms for panes expanding along one axis. Respect `prefers-reduced-motion`.

## Copy

Plain, exact, short. Name the reason for every refusal ("Preview is 6 minutes old. Refresh it before approving."). Never paraphrase SQL or counts.
