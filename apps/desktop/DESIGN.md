# Forja desktop: the visual system

How the app looks and why, for anyone adding or changing a screen. The styles are in
`src/renderer/src/styles/`, the shared components in `src/renderer/src/components/`.

## Principles

- **Quiet chrome.** Neutral graphite surfaces, hairline borders, one accent. The user's images and
  3D assets carry the colour. No texture, glow, ornament, or illustration that is not information.
- **One accent, used sparingly.** Amber marks the primary action, the current selection, focus and
  progress. Green, yellow and red appear only on status.
- **Sentence case everywhere.** The one exception: 11px column headers of tables and dense lists
  are uppercase with 0.04em tracking (`--t-head`).
- **Dense but breathable.** 13px text, 32px controls, a 4px grid. Group with space and one
  hairline: never a bordered card inside a bordered card.
- **Hierarchy by weight and colour, not size.** Three text colours, weights 400/500 (600 for
  titles), a short type scale.
- **Motion is feedback.** Every state change animates, with `transform` and `opacity` only.

## Tokens (`styles/tokens.css`)

| Token | Value | Use |
|---|---|---|
| `--bg` | `#0f1012` | Window. The Electron window's `backgroundColor` in `src/main/index.ts` must match. |
| `--bg-deep` | `#0b0c0d` | Sidebar, viewer, thumbnail wells, inputs |
| `--surface` | `#16181b` | Cards, panels, rows |
| `--surface-2` | `#1d2024` | Hover, selected, raised (menus, toasts, default buttons) |
| `--surface-3` | `#262a2f` | Pressed, active control, progress tracks |
| `--border-soft` | `#202328` | Row rules, quiet dividers |
| `--border` | `#2b2f35` | Card and control borders |
| `--border-strong` | `#3d434b` | Hovered control, dashed drop targets |
| `--text` / `--text-2` / `--text-3` | `#e9ebee` / `#a8adb5` / `#7e848d` | Primary / secondary / meta. Nothing dimmer than `--text-3`. |
| `--accent` / `--accent-hi` / `--accent-soft` / `--on-accent` | `#f0a04b` … | Primary action, selection, focus, progress |
| `--ok` / `--warn` / `--bad` (+ `-soft`, `--bad-hi`) | green / yellow / red | Status only |
| `--shadow` | | Menus, popovers, dialogs, toasts. Nothing else casts a shadow. |
| `--r-1` / `--r-2` / `--r-3` | 6 / 8 / 12px | Controls and pills / cards, panels, thumbnails / dialogs, the drop target, the viewer |
| `--s-1` … `--s-7` | 4, 8, 12, 16, 24, 32, 48px | Spacing |
| `--h-sm` / `--h` / `--h-lg` | 28 / 32 / 36px | Control heights: dense rows and toolbars / default / a screen's one call to action |
| `--sidebar`, `--column` | 216px, 920px | Sidebar width; the widest a form or a list of rows gets |

Type is set with `font` shorthands: `font: var(--t-body)`.

| Token | Size/line, weight | Use |
|---|---|---|
| `--t-caption` | 12/16, 400 | Meta, hints, a row's second line |
| `--t-body` | 13/20, 400 | Default text |
| `--t-strong` | 13/20, 500 | Labels, button text, row titles |
| `--t-title` | 14/20, 600 | Card and dialog titles |
| `--t-section` | 12/16, 600 | Section headings, in `--text-2` |
| `--t-screen` | 20/28, 600 | A screen's `h1` |
| `--t-head` | 11/16, 600 | Column headers (uppercase, 0.04em) |
| `--t-mono` | 12/16 mono | Ids, hashes, paths. For numbers that must line up in the UI font, use `.num`. |

Fonts are Inter and JetBrains Mono, bundled from `@fontsource-variable`. No italics.

## Layout

- **Sidebar**: brand row, five 32px nav rows with a sliding ground for the current one, one
  status line at the bottom.
- **Screen** (`.screen`): padding 24/32/48px. Add `.capped` when the content is a form or a list
  of rows (Queue, Models, Settings): its children stop at `--column`. Grids and workspaces use
  the full width.
- **`ScreenHead`** (`title`, `sub`, children as the actions on the right), then content.
- **`SectionHead`** inside a `.section`: a quiet 12px label, 28px above, 12px below.
  `.section-note` is the one line under it that says what the section is for.
- The window's minimum size is 960x600: check a screen there. Use a container or media query to
  restack, never a horizontal scrollbar.

## Shared primitives

Components are in `components/Controls.tsx` unless another file is named; classes are in
`styles/controls.css`.

| Primitive | How | When |
|---|---|---|
| Button | `.btn`; `.primary`, `.ghost`, `.danger`; sizes `.sm`, `.lg`; `.icon` for icon-only (give it an `aria-label`) | `.primary` once per view at most: the action the view exists for. `.ghost` for the lesser action beside it. `.sm` inside rows and toolbars. |
| Text action | `.text-btn` | An action inside a sentence or caption |
| Disclosure | `.disclosure` + `aria-expanded` + `ChevronIcon` | Show or hide details in place |
| Input | `.input` on `input`, `select`, `textarea`; `.sm`; `NumberInput`, `TextInput` (commit on Enter or blur) | `select.input` draws its own chevron |
| Field | `label.field` (a span, then the input) | Label over input, in dialogs |
| Search | `label.search` with `SearchIcon` and `.input` | |
| Switch | `<Switch>` | A boolean that applies at once. Name it with children, `label`, or a `<label htmlFor>`. |
| Choices | `<Choices>` (`.choices` / `.choice`; `.two` for two columns) | One of 2 to 5 options that deserve a line of help each |
| Hint | `<Hint>` | The `?` beside an option whose effect is not obvious |
| Pill | `<Pill tone dot live>`, `<StatusPill status>` | A state or a short tag. `StatusPill` for job, task and benchmark states, so they read the same everywhere. No tone for neutral tags. |
| Dot | `.dot` (`.ok`, `.bad`, `.live`) | A state with no room for words; give it a label for screen readers |
| Progress | `<Progress value label>` (`.bar`) | 0..1, or indeterminate without a value |
| Stage track | `<StageTrack>` (`components/StageTrack.tsx`, `styles/queue.css`) | Anything that shows a job's pipeline |
| Thumbnail well | `.thumb` | A 48px input image or preview, never cropped |
| Menu | `<MoreMenu>` + `<MenuItem>` (`components/Menu.tsx`); `.menu-anchor > .menu` for a menu under its own button (see `ExportMenu`) | Rare actions behind a "more" button; a destructive item gets `danger` |
| Dialog | `<Dialog title size alert keepOpen>` + `<DialogActions>` (`components/Dialog.tsx`) | Every modal. It handles Escape, the scrim, the focus trap and returning focus. Pass `keepOpen` while typed work would be lost. Primary button last. |
| Toast | `useToast()(text, bad?, action?)` (`lib/toast.tsx`) | The result of an action the user just took. Not for state that should stay on screen. A notice leaves by itself; an error (`bad`) stays until dismissed. The same words show once, four toasts at most. |
| Table | `.table`; `.compact`, `.flush`; `.n` for numeric cells | Tabular data. `.flush` when it stands in a column with no panel around it. |
| Group | `.group` | One bordered panel of rows with hairlines between them: settings, providers, jobs, models |
| Label/value rows | `dl.kv` > `.kv-row` > `dt` + `dd` | Properties of one thing |
| Skeleton | `.skeleton` with the final layout's size | Loading. Never a spinner. |
| Empty state | `<Empty icon title action>` (`components/Empty.tsx`) | Nothing to show: say what is missing and how it gets here |
| Error block | `<ErrorBlock>` | Something failed: a plain sentence on what happened and what to do |
| Notice | `.notice` (a `div` with `strong` + `span`, then a button) | A call to action beside the sentence that explains it |
| Caution | `.caution` | A warning inside a row: a stand-in result, a license limit |

Utilities (`styles/base.css`): `.row`, `.grow`, `.muted` (`--text-2`), `.dim` (`--text-3`),
`.small`, `.mono`, `.num`, `.truncate`, `.selectable`, `.sr-only`.

## Motion (`styles/motion.css`)

- Animate `transform` and `opacity` only. A hover or selected ground may change colour at once,
  without a transition; to fade a border or a tint in, draw it on a pseudo-element and animate
  that element's opacity.
- Durations: `--dur-1` 120ms (hover, press), `--dur-2` 180ms (state changes), `--dur-3` 260ms
  (enter, screen change). Easing: `--ease-out` by default. Nothing overshoots.
- `.enter` makes new content rise 4px and fade in; `style={{ '--i': index }}` staggers a list.
- Screen changes go through `transition()` in `lib/nav.ts` (the View Transitions API).
- `prefers-reduced-motion` zeroes the durations; an infinite animation must also be switched off
  in a `@media (prefers-reduced-motion: reduce)` block next to its rule.

## Icons

`components/icons.tsx` re-exports `lucide-react` icons under the name of what they mean here
(`DeleteIcon`, `ExportIcon`, `QueueIcon`), at 16px with a 1.75 stroke, hidden from assistive
technology. Import icons from that file only, so one can be swapped in one place; add a new one
there with a name that says its meaning, not its shape. `BrandMark` is the app's emblem and the
only drawing that is not from lucide. An icon never stands alone as a control without an
`aria-label` on the control.

## CSS file map

`styles/app.css` only imports the others, in this order. Each rule lives in exactly one file.

| File | Owns |
|---|---|
| `tokens.css` | Every colour, radius, space, size and type token |
| `motion.css` | Durations, easings, keyframes, `.enter`, the screen transition |
| `base.css` | Reset, type, scrollbars, focus ring, utilities |
| `shell.css` | App grid, sidebar, `.screen`, screen and section heads, `.back` |
| `controls.css` | Every shared primitive above |
| `generate.css` | The workspace, drop target, settings form, preparing and views cards, the views sheet |
| `queue.css` | Job rows, the stage track and the stage list |
| `library.css` | The asset grid, cards, the selection toolbar |
| `asset.css` | Asset detail: the viewer and the inspector |
| `models.css` | Model lists, task progress, checks, the guided setup, the hub catalog |
| `settings.css` | Setting rows, this computer's facts, providers, sharing, cloud, prices |
| `bench.css` | The benchmark section |

A class used by one screen goes in that screen's file. A component drawn on several screens keeps
its rules in the file of the area it belongs to (the stage track in `queue.css`, setup and checks
in `models.css`). A class that is a general control goes in `controls.css`.

## Do and don't

- Do reuse a primitive before writing a new class; if two screens need the same new thing, it is
  a primitive: add it to `controls.css` and to this file.
- Do use tokens for every colour, radius and space. A raw hex value in a screen's CSS is a bug.
- Do give every list its loading (skeleton), empty (`Empty`) and error (`ErrorBlock`) states.
- Do write copy in plain words and sentence case: "Queue", "Delete 3 assets?", "1 asset".
- Don't put a bordered card inside a bordered card. Inside a panel, separate with a hairline.
- Don't use the accent for decoration, or a status colour for anything that is not a status.
- Don't add a shadow, gradient, glow, texture, illustration, italic or letter-spaced capitals.
- Don't animate `width`, `height`, `top`, `background` or `border-color`.
- Don't import from `lucide-react` outside `components/icons.tsx`.
- Don't hand-roll a dialog, menu or toast: use the shared ones, which carry the keyboard and
  focus behaviour.
