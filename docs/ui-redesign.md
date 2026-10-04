# UI redesign plan

The web UI is rebuilt so that it looks and behaves like Supabase Studio,
which most of our users already know. Six phases, each one PR. The
reference screenshots are the Table Editor, the SQL Editor and the
expanded navigation rail of Supabase Studio, as of late 2026.

## Decisions

| # | Decision |
| --- | --- |
| 1 | Order: the shell first, then the Table Editor, then the SQL Editor and the remaining pages. One PR per phase, each releasable on its own. |
| 2 | Supabase features PGDock has no backend for (RLS and the "Role" switch, auth, storage, edge functions, realtime, AI filters, CSV import, charts of results) are **left out**, not shown disabled. |
| 3 | Dark theme by default, with a light theme and "system" in the user menu. **Purple** accent. |
| 4 | Layout, density and interaction follow Supabase closely. Supabase's logo, name and green brand are not used: PGDock keeps its own mark. |
| 5 | Same stack (React 19, Vite, Tailwind 4, TanStack Router and Query). New libraries, all MIT: Radix UI primitives with shadcn-style components, `react-data-grid` (what Supabase's grid is built on), `lucide-react` icons, `cmdk` (the ⌘K menu), `sonner` (toasts), and **Monaco** (`monaco-editor`, the editor Supabase uses) in place of CodeMirror. The font is Inter, bundled with `@fontsource` so installs need no outside network access; Monaco is bundled too (no CDN loader), in its own chunk. |
| 7 | SQL queries are saved on the server (question 1, option a): per project, private or shared with the project's members, with per-user favourites (phase 3). |
| 8 | PGDock gets a simple SVG mark of its own, drawn in phase 1. |
| 9 | The branch switcher works like Supabase's: `main` carries a `PRODUCTION` badge, branches a `PREVIEW` badge; the menu searches branches and has "Create branch" and "Manage branches". The project's tier shows on the Overview and in Settings, not in the switcher. |
| 10 | Branches, backups, webhooks, scheduled jobs, extensions and migrations live under the rail's Database section. |
| 6 | No API changes, except small additions a phase can't do without; each is listed in its phase. |

## Design system (phase 1)

**Tokens**, CSS variables on `:root` with a `.dark` override, read by
Tailwind's `@theme`:

- **Neutrals:** a 12-step grey scale from Supabase's dark palette, used for
  the app background (#121212), panels (#171717), raised surfaces (#1f1f1f)
  and borders (#2e2e2e), with a matching light scale.
- **Accent:** purple, from violet 500–600 (#8b5cf6 / #7c3aed), for primary
  buttons, focus rings and the selected item. A muted tint is used for
  selected rows and the active rail item.
- **States:** red for destructive actions and the "danger" button (the
  "RLS disabled" style), amber for warnings and badges like PRODUCTION,
  green for success.
- **Type:** Inter 13px in the UI and 14px for body text, with JetBrains
  Mono or the system monospace in the grid and editors. Uppercase 11px
  labels with letter-spacing for section headings ("PRIVATE (3)").
- **Shape:** 6px radius on controls, 1px borders, almost no shadows except
  on popovers and side panels. Compact controls: 26–28px high buttons and
  inputs, as in Supabase.

**Components** (`web/src/components/ui/`, one file each):

| Component | Notes |
| --- | --- |
| Button | `primary` (purple), `default` (bordered), `ghost`, `danger`, `warning`; sizes `tiny`, `small`, `medium`; optional icon and keyboard hint (`Run ⌘↵`). |
| Input, Textarea, Select, Combobox, Checkbox, Switch, Radio | Inputs can carry a leading icon (search) and a trailing addon. |
| DropdownMenu, ContextMenu, Popover, Tooltip | Radix. |
| Dialog and AlertDialog | Confirmations, including type-to-confirm. |
| SidePanel (sheet) | Slides in from the right, full height, with a header, scrolling body and a sticky footer (Cancel / Save). Used by everything that creates or edits, as in Supabase. |
| Tabs | Underline tabs ("Results", "Chart"); segmented tabs ("Data / Definition"). |
| Badge | Pill and uppercase variants (`FREE`, `PRODUCTION`, `UNRESTRICTED`). |
| Table | Plain lists (members, backups, tokens): flush to the content, hover rows, no cards. |
| EmptyState, Skeleton, Spinner, Alert/Admonition, CopyField, KeyValue | |
| Toaster | `sonner`, replacing the current toasts. |
| CommandMenu | ⌘K: jump to a project or page, run actions ("New project", "Open SQL editor", "Toggle theme"). |

A development-only gallery at `/_ui` renders every component in both
themes, for review and for later visual tests.

## The shell (phase 1)

Laid out as in the screenshots:

- **Top bar** (48px): PGDock mark, then breadcrumb-style switchers with
  `/` between them:
  - **Organisation**, with its plan as a badge (`PERSONAL`, `TEAM`,
    `UNLIMITED`). The menu lists organisations and has "New organisation".
  - **Project**, with a menu that searches the organisation's projects
    and has "New project".
  - **Branch**, as in Supabase: `main` (the project itself) with a
    `PRODUCTION` badge, or the branch's name with a `PREVIEW` badge. The
    menu searches the project's branches and ends with "Create branch" and
    "Manage branches". Choosing a branch opens the same page on it.
  - **Connect** button, which opens the connection dialog (the current
    Connect page, as a dialog).

  On the right: Search ⌘K, a docs link, an alerts indicator (platform
  admins), and the avatar menu (account, theme, sign out).
- **Icon rail** (48px): icons with tooltips. Hovering expands it over the
  content with labels, as in the third screenshot; a pin at the bottom
  keeps it expanded. Groups are separated by rules (below).
- **Section sidebar** (240px): the current section's own menu, title at
  the top ("Table Editor", "SQL Editor", "Database"). It can be collapsed
  with the toggle at the top of the content, as in Supabase.
- **Content** fills the rest, edge to edge. A page has an optional header
  (title, description, actions) and no cards around lists.

### Navigation map

**Inside a project** (`/projects/$id/…`):

| Rail | Section sidebar | Today's page |
| --- | --- | --- |
| Project Overview | — | Overview |
| Table Editor | schema picker, New table, table list | Tables |
| SQL Editor | saved queries (see question 1), templates | SQL |
| *rule* | | |
| Database | Tables (structure), Extensions, Branches, Backups, Webhooks, Scheduled jobs, Migrations | Branches, Backups, Webhooks, Jobs, extensions card |
| *rule* | | |
| Reports | Database (metrics), Query performance (top queries) | Metrics |
| Logs | Operations (the project's), Reaped statements | operations, reaper log |
| *rule* | | |
| Project Settings | General, Compute and tier (promote/demote), Members, Backup storage and key, API tokens (link), Danger zone | Settings, Members |

**Outside a project** (`/projects`, `/org/…`), the organisation's
sections: Projects, Team, Usage & quotas, Backup storage, API tokens,
Audit log, Organisation settings.

**Platform admins** get a "Platform" entry in the organisation menu and
the avatar menu, which switches the rail to: Nodes, Organisations, Users,
Plans, Dedicated requests, Alerts, Operations, Platform audit, Platform
settings.

Every current URL keeps working, either at the same path or redirected,
so links in emails and docs stay valid.

## Phases

### Phase 1: Design system and shell

- The tokens, components, theme (dark default, light, system) and the
  `/_ui` gallery.
- The top bar with its switchers, the icon rail with hover expansion and
  pin, the section sidebars, the ⌘K menu, and the Connect dialog.
- Every existing page moves into the new shell **unchanged inside**: the
  pages restyle themselves in later phases. Old cards and buttons pick up
  the new tokens, so nothing looks broken in between.
- Public pages (sign in, sign up, setup, invitations) get the new
  two-column auth layout here as well, because the shell work touches
  them anyway.

**Done when:** every route renders in the new shell in both themes; the
rail, switchers and ⌘K reach every page the old menu reached; the e2e
journeys pass (updated only where navigation changed: menus replace links,
not test ids).

### Phase 2: Table Editor

Matches the first screenshot:

- **Sidebar:** a schema dropdown, "New table", table search with a filter
  menu (show views, materialized views, foreign tables), and the table list
  with type icons and a "⋮" menu (Edit table, Duplicate table, Copy name,
  Export CSV, Delete table). There's no `UNRESTRICTED` badge (RLS is out of
  scope).
- **Tabs** for open tables, with "+" to open another, kept across reloads
  (per user, in the browser).
- **Toolbar:** filter box and Filter popover (conditions row by row:
  column, operator, value), Sort popover (columns and direction), refresh,
  and an Insert menu (row, column, import data; import is left out).
- **Grid** (`react-data-grid`):
  - Sticky header with the column name, type and key icons, and a column
    menu (Edit column, Sort ascending/descending, Copy name, Delete
    column).
  - A checkbox column; "+" at the end of the header adds a column.
  - Resizable, reorderable columns (widths remembered per table).
  - Double-click or Enter edits a cell in place, typed by column: text,
    number, boolean dropdown, date and time, enum dropdown, `json`/`jsonb`
    in a side editor, `NULL` and `DEFAULT` choices.
  - Foreign key cells link to the referenced row.
  - Selecting rows shows "Delete N rows" and "Copy" in the toolbar.
  - Edits are saved through the existing row-change API, and xmin
    conflicts are shown as Supabase-style toasts with "Reload row".
- **Footer:** page navigation, page size (100/500/1000), the record count,
  and a Data / Definition toggle. Definition shows the table's DDL
  (read-only, CodeMirror).
- **Side panels:**
  - **Create / edit table:** name, description, the column list (name,
    type picker with search and groups, default value with suggestions such
    as `now()`, `gen_random_uuid()` and identity, primary key; each row's
    "⚙" opens nullable, unique, check, array and foreign key settings), and
    a foreign key relation editor (referenced schema, table and columns,
    on update / on delete).
  - **Add / edit column:** the same column form.
  - **Insert / edit row:** one field per column, by type, with defaults
    and nullability shown, and a JSON editor for `json`/`jsonb`.
  - Each schema change shows the existing risk notes and the SQL it will
    run before applying, as now (the review step Supabase lacks is kept: it
    is a PGDock safety feature).
- Read-only members and read-only projects see the same screens without
  editing controls.

**API additions (built):** numbered pages of up to 1,000 rows (`offset`,
`limit`) and several sort columns (`order`) on the rows and export
endpoints; `GET …/tables/{schema}/{table}/count` (exact, or the planner's
estimate above 50,000 unfiltered rows or after 5 seconds) and
`…/definition` (the DDL); table and column comments and each
constraint's columns in the table info; enum types in the schema tree;
and new schema changes: inline column constraints (`unique`, `check`,
`references`), `set_comment`, `duplicate_table` (structure, optionally
with the rows) and `batch` (changes to one table in one transaction,
which the Edit table panel uses).

**Differences from Studio, on purpose:** every schema change goes
through the review dialog (SQL, risk notes, migration export); the
toolbar has no free-text filter box (the Filter popover covers it); the
JSON cell editor is a plain text area until Monaco arrives in phase 3;
and "+" in the tab bar starts a new table.

**Done when:** a user who knows Supabase can browse, filter, sort, edit
cells and rows, insert and delete rows, create a table with a foreign key,
and add, edit and drop columns without explanation. Unit tests cover the
cell editors and the filter builder, and the e2e table-editing journey is
rewritten for the new screens.

### Phase 3: SQL Editor

Matches the second screenshot:

- **Sidebar:** query search and "+", collapsible groups (Private, Shared,
  Favorites; see question 1), and Templates (common snippets PGDock
  ships).
- **Tabs** for open queries.
- **Editor:** Monaco, as in Supabase, with a theme to match: line
  numbers, SQL highlighting, completion of schemas, tables and columns
  from the project's schema, format (⌘⇧F), and run (⌘↵ for all, or the
  selection). The table editor's Definition view and JSON cell editor use
  Monaco too (read-only and JSON mode). The toolbar has Saved, Format, a
  favourite toggle, the console's read-only role switch (our nearest
  match to Supabase's role picker), and Run.
- **Results pane** in a resizable split: the result grid (the Table
  Editor's grid, read-only), row count and timing, notices, errors with
  the position highlighted, Export CSV/JSON, and Cancel for long queries.
  There is no "Chart" tab (out of scope).

**Saved queries (API addition):**

- `saved_queries`: project, org, owner, name, SQL, visibility (`private`
  or `shared`), timestamps.
- `saved_query_favorites`: user and query.
- Routes: `GET/POST /projects/{id}/queries` and
  `GET/PATCH/DELETE /projects/{id}/queries/{query_id}`, plus a favourite
  toggle.
- Permissions: anyone who can read the console sees their own queries and
  the shared ones. Owners edit and delete their own; project admins may
  also delete shared ones.
- Org-scoped like every tenant table (the lint applies). Queries are
  deleted with the project and moved with a transfer. Saved by debounce
  as you type ("Saved" in the toolbar).

**Built:** as above, with two notes. Monaco is bundled with the app
(PostgreSQL and JSON only), and loads only on the SQL Editor, or when the
table editor's Definition view or JSON editor first opens; its chunk is
about 900 kB gzipped, the shell is unchanged. The "role switch" is a
picker between the owner role (read/write) and read-only.

**Done when:** queries run, cancel and export as today; queries save,
share, favourite and reopen across devices; the e2e SQL journey passes.

### Phase 4: Project pages

- **Overview:** status, tier, size and connection summary; small metric
  charts; recent operations; Connect.
- **Database section:**
  - Branches, Backups (with Download and Restore), Webhooks, Scheduled
    jobs, and Extensions, in the same list style as Supabase's Database
    pages.
  - Creating and editing happens in side panels.
  - Migrations: export the schema changes made in the table editor.
- **Reports:** the metric charts, restyled (axes, tooltips and colours
  from the tokens).
- **Logs:** the project's operations, with live step logs.
- **Project Settings:** General, Compute and tier (the promote and demote
  wizards in side panels), Members, Backup storage and key, Danger zone.

**Done when:** every project page is in the new style, no old `Card`
layout remains in project pages, and the e2e journeys pass.

### Phase 5: Organisation, account and admin

- Projects as a grid of cards, like Supabase's organisation home: name,
  tier, region or node, status, last backup. There's also a "New project"
  flow in Supabase's style (name, tier, size, then the credentials) and
  Import.
- Team, Usage & quotas (bars and the hourly chart), Backup storage, API
  tokens, Audit log, and Organisation settings (including deletion).
- Account: profile, security (password, two-factor, recovery codes),
  sessions, tokens and invitations.
- The platform admin area: Nodes, Organisations, Users, Plans, Requests,
  Alerts, Operations, Audit and Settings, in the same list and side-panel
  style.

**Done when:** no page uses the old layout components; they are deleted.

### Phase 6: Polish

- Keyboard shortcuts across the app (shown in tooltips and in a "?"
  sheet).
- Empty states and loading skeletons everywhere.
- Focus and contrast pass in both themes (WCAG AA).
- Narrow screens: the rail becomes a drawer and side panels go full
  width.
- Visual regression screenshots of the key screens in the e2e run.
- The documentation's UI paths and screenshots updated.

**Done when:** an accessibility pass (axe) shows no serious issues on the
key screens and the screenshots are reviewed.

## Ground rules for every phase

- **Tests:** keep `data-testid`s where a control survives. The e2e
  journeys are updated in the same PR, not disabled. New components get
  Vitest tests where they have logic (filter builder, cell editors, type
  picker).
- **No dead ends:** while a phase is in progress, pages it hasn't reached
  still work in the new shell.
- **Bundle size:** the Table and SQL editors are split into their own
  chunks; the shell stays under about 250 kB gzipped.
- **Security unchanged:** the UI still relies on the API for every
  permission and hides only what the API would refuse; no new secrets in
  the browser.
