# Forja desktop: the visual system

How the app looks and why, for anyone adding or changing a screen. The styles are in
`src/renderer/src/styles/`, the shared components in `src/renderer/src/components/`.

## Principles

- **A working forge, not a tavern sign.** The layout is a calm tool UI. The theme lives in five
  places only, each listed under [The forge theme](#the-forge-theme): material colour, one display
  face, a drawn icon set, four shapes and six motions. Anything else that looks "fantasy" is a bug.
- **Soot, iron, ember.** Warm near-black surfaces, hairline borders, one accent. The user's images
  and 3D assets carry the colour. No texture, and no illustration that is not information.
- **One accent, used sparingly.** Ember marks the primary action, the current selection, focus and
  work in progress. Green, yellow and red appear only on status. Brass is ornament and never
  carries text or a state; steel means a finished stage and nothing else.
- **Sentence case everywhere.** Two exceptions, both 11px and uppercase: column headers of tables
  and dense lists (`--t-head`, 0.04em) and section labels (`--t-section`, 0.08em).
- **Dense but breathable.** 13px text, 32px controls, a 4px grid. Group with space and one
  hairline: never a bordered card inside a bordered card.
- **Hierarchy by weight and colour, not size.** Three text colours, weights 400/500 (600 for
  titles), a short type scale.
- **Motion is feedback.** Every state change animates, with `transform` and `opacity` only.

## The forge theme

| Where | What | The rule |
|---|---|---|
| Colour | Soot and iron surfaces, ember accent, brass, steel | See the tokens below. |
| Type | Marcellus (`--display`) | The brand name, a screen's `h1`, dialog titles and the asset name in its stat block. Nowhere else: every label, row and sentence is Inter. |
| Icons | `components/icons.tsx` | Drawn for Forja; see [Icons](#icons). |
| Chamfer | Two cut corners (`clip-path`) | The primary button only, so once per view. Every other control is a plain rectangle with `--r-1`. |
| Brackets | Brass corners (`.brackets`) | The two surfaces the app exists for: the image drop target and the 3D viewer. |
| Diamond | A square turned 45 degrees | Every dot: pills, `.dot`, section bullets, stage rivets, the "more" icon. Nothing in the chrome is a circle. |
| Slant | `skewX(-24deg)` | Stage segments, like bar stock. Cold iron (`--surface-3`) waits, ember (`--heat`) is being worked, steel (`--steel`) is finished. |
| Stat block | Double brass bar, tapered rules, a grid of stats | The asset inspector only: the one tabletop quotation in the app. |

Words: the running state is "Forging", the queue's running section is "On the anvil", and the
primary action is "Forge". Navigation, settings and errors stay plain.

## Tokens (`styles/tokens.css`)

| Token | Value | Use |
|---|---|---|
| `--bg` | `#131110` | Window. The Electron window's `backgroundColor` in `src/main/index.ts` must match. |
| `--bg-deep` | `#0d0c0b` | Sidebar, viewer, thumbnail wells, inputs |
| `--surface` | `#1a1816` | Cards, panels, rows |
| `--surface-2` | `#23201d` | Hover, selected, raised (menus, toasts, default buttons) |
| `--surface-3` | `#2d2a26` | Pressed, active control, progress tracks, a stage that waits |
| `--border-soft` | `#25221f` | Row rules, quiet dividers |
| `--border` | `#34302b` | Card and control borders |
| `--border-strong` | `#4b463f` | Hovered control, dashed drop targets |
| `--text` / `--text-2` / `--text-3` | `#ece7df` / `#b3ab9f` / `#8f877b` | Primary / secondary / meta. Nothing dimmer than `--text-3`. |
| `--accent` / `--accent-hi` / `--accent-deep` / `--accent-soft` / `--on-accent` | `#f0934a` … | Ember: primary action, selection, focus, progress |
| `--heat` | gradient, deep to hi | Work in progress: a running stage's fill, a progress bar |
| `--brass` | `#b99a5e` | Ornament only: brackets, section bullets, the stat block's rules |
| `--steel` | `#9fb1ba` | A finished stage |
| `--ok` / `--warn` / `--bad` (+ `-soft`, `--bad-hi`) | green / yellow / red | Status only |
| `--shadow` | | Menus, popovers, dialogs, toasts. Nothing else casts a shadow. |
| `--r-1` / `--r-2` / `--r-3` | 3 / 4 / 6px | Controls / cards, panels, thumbnails / dialogs. Pills are 2px; the bracketed surfaces are square. |
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
| `--t-section` | 11/16, 600 | Section labels: uppercase, 0.08em, in `--text-2`, with a brass diamond and a rule |
| `--t-screen` | 26/32, Marcellus 400 | A screen's `h1` |
| `--t-dialog` | 18/24, Marcellus 400 | Dialog titles |
| `--t-head` | 11/16, 600 | Column headers (uppercase, 0.04em) |
| `--t-mono` | 12/16 mono | Ids, hashes, paths. For numbers that must line up in the UI font, use `.num`. |

Fonts are Inter and JetBrains Mono (`@fontsource-variable`) and Marcellus (`@fontsource/marcellus`),
all bundled. No italics. Numbers that are measured (times, counts, a stat) use the mono face.

## Layout

- **Sidebar**: brand row, five 34px nav rows with a sliding ground and ember bar for the current
  one, one status line at the bottom. While something generates or installs, the status flame is
  lit and the corner behind it glows; idle, both are cold.
- **Screen** (`.screen`): padding 24/32/48px. Add `.capped` when the content is a form or a list
  of rows (Queue, Models, Settings): its children stop at `--column`. Grids and workspaces use
  the full width.
- **`ScreenHead`** (`title`, `sub`, children as the actions on the right), then content.
- **`SectionHead`** inside a `.section`: a brass diamond, an 11px uppercase label and a hairline
  to the edge, 28px above, 12px below.
  `.section-note` is the one line under it that says what the section is for.
- The window's minimum size is 960x600: check a screen there. Use a container or media query to
  restack, never a horizontal scrollbar.

## Shared primitives

Components are in `components/Controls.tsx` unless another file is named; classes are in
`styles/controls.css`.

| Primitive | How | When |
|---|---|---|
| Button | `.btn`; `.primary` (chamfered, ember), `.ghost`, `.danger`; sizes `.sm`, `.lg`; `.icon` for icon-only (give it an `aria-label`) | `.primary` once per view at most: the action the view exists for. `.ghost` for the lesser action beside it. `.sm` inside rows and toolbars. |
| Text action | `.text-btn` | An action inside a sentence or caption |
| Disclosure | `.disclosure` + `aria-expanded` + `ChevronIcon` | Show or hide details in place |
| Input | `.input` on `input`, `select`, `textarea`; `.sm`; `NumberInput`, `TextInput` (commit on Enter or blur) | `select.input` draws its own chevron |
| Field | `label.field` (a span, then the input) | Label over input, in dialogs |
| Search | `label.search` with `SearchIcon` and `.input` | |
| Switch | `<Switch>` | A boolean that applies at once. Name it with children, `label`, or a `<label htmlFor>`. |
| Choices | `<Choices>` (`.choices` / `.choice`; `.two` for two columns) | One of 2 to 5 options that deserve a line of help each |
| Hint | `<Hint>` | The `?` beside an option whose effect is not obvious |
| Pill | `<Pill tone dot live>`, `<StatusPill status>` | A state or a short tag. `StatusPill` for job, task and benchmark states, so they read the same everywhere. No tone for neutral tags. |
| Brackets | `.brackets` | Brass corners, drawn by the host's `::before`. The drop target and the viewer only. |
| Dot | `.dot` (`.ok`, `.bad`, `.live`) | A diamond: a state with no room for words; give it a label for screen readers |
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
- The theme's six motions, and the only ones that are not plain feedback:

  | Name | What moves | When |
  |---|---|---|
  | Heat (`heat`, `sweep`) | A running stage's fill pulses and a light crosses it | While a stage runs |
  | Quench | A finished stage's ember fill fades to show steel | Once, when a stage succeeds |
  | Strike | The primary button scales to 0.97 and flashes | On press |
  | Sparks (`spark`) | Five sparks leave a job's thumbnail | Once, when a job on screen finishes |
  | Kindle (`flicker`, `glow`) | The status flame flickers, the sidebar corner glows | Only while generating or installing |
  | Slide and rise | The nav ground slides; new content rises 4px | Navigation, new rows |

  A loop runs only while real work does: an idle app is still.
- `.enter` makes new content rise 4px and fade in; `style={{ '--i': index }}` staggers a list.
- Screen changes go through `transition()` in `lib/nav.ts` (the View Transitions API).
- `prefers-reduced-motion` zeroes the durations; an infinite animation must also be switched off
  in a `@media (prefers-reduced-motion: reduce)` block next to its rule.

## Icons

`components/icons.tsx` holds every icon, drawn for Forja and named for what it means here
(`DeleteIcon`, `ExportIcon`, `QueueIcon`): 16px by default, hidden from assistive technology.
There is no icon library. Import icons from that file only, and add a new one there with a name
that says its meaning, not its shape. An icon never stands alone as a control without an
`aria-label` on the control.

Drawing one:

- A 24 unit grid, a 1.75 stroke, square caps, mitred joins, no fill.
- Straight segments only: a corner is cut at 45 degrees, never rounded; a circle becomes an
  octagon or a diamond.
- Keep 2.5 units clear of the edge; snap points to half units.
- At most one filled detail. A `Solid` detail takes the icon's colour. A `Hot` detail is grey
  until an ancestor sets `--icon-hot`: the current nav row lights its icon this way, and the
  status flame is lit while the forge works.
- Prefer the forge's own objects where they are understood at a glance (an anvil for Generate,
  ingots for Queue, a chest for Library, a tome for Models); keep the conventional shape where a
  metaphor would cost a second of thought (search, delete, close, back).

`BrandMark` is the app's emblem, traced from the app icon.

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
| `asset.css` | Asset detail: the viewer and its bracketed frame (`.viewer-frame`), the inspector as a stat block (`.stat-bar`, `.stat-taper`, `.stat-grid`, `.stat`) |
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
- Don't add a theme element that is not in [The forge theme](#the-forge-theme): no second chamfer
  on a view, no brackets on a card, no display face in a label, no brass text.
- Don't add a shadow, texture, illustration or italic. The only gradients are `--heat`, the
  primary button's and the sidebar glow.
- Don't run a looping animation while nothing is working.
- Don't animate `width`, `height`, `top`, `background` or `border-color`.
- Don't add an icon library, or draw an icon outside `components/icons.tsx`.
- Don't hand-roll a dialog, menu or toast: use the shared ones, which carry the keyboard and
  focus behaviour.
