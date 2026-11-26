# Plugin store: design

Date: 2026-11-26. Status: draft for approval.

## Intent

People who use Second Life often never open a terminal. Today, adding Linear, Notion or Context7 to Claude means
finding the plugin, typing `claude plugin install …` and knowing that `/mcp` is where you sign in. The owner wants a
page in the browser, like Choose models, where a person sees Anthropic's plugin catalog, ticks what they want and
gets it installed. Second Life sends Claude to other models, so the claude.ai connectors never load; plugins are how
these people get the same services.

## Goals

- A browser page that lists the plugins Claude Code can install, with search.
- One click installs a plugin for the user (all projects); one click removes it.
- The page shows what is already installed and keeps it in sync with Claude Code.
- After an install, the page says what is left to do: for a plugin with MCP servers that need a sign-in, open Claude
  and use `/mcp`.
- The page works for a person who has never used a terminal.

## Non-goals (this version)

- Chat and Cowork in Claude Desktop. The Code tab of Desktop loads plugins installed in Claude Code, so it is covered;
  Chat and Cowork take plugins only from Desktop's own marketplace list and are a later step ("variant B").
- Our own plugins: Gmail with a Google key, Peekaboo. They come later as extra cards.
- Plugin updates, enabling and disabling, project scope, plugin options (`claude plugin configure`).
- Marketplaces other than Anthropic's two.

## Anti-goals

- Second Life does not download, unpack or write plugin files itself. Claude Code does all of that.
- Nothing is installed without a click on that plugin.
- The page never hides that a plugin runs code on the computer: the install confirmation says so.
- No command runs in a project folder, where project settings would apply.

## Facts this rests on (checked 2026-11-26, Claude Code 2.1.325)

- `claude plugin list --available --json` prints `{"installed": [...], "available": [...]}` in about 4 s.
  - Each available entry has `pluginId`, `name`, `description`, `marketplaceName`, `source`, sometimes `version` and
    `installCount`.
  - Each installed entry has `id`, `version`, `scope`, `enabled` and `mcpServers`.
- On this Mac it lists 4,099 plugins: 316 from `claude-plugins-official` and 3,783 from `anthropic-plugin-directory`.
  The directory is built into Claude Code ("Built in (Anthropic Directory)").
- In an empty `CLAUDE_CONFIG_DIR`, without a claude.ai login, these commands worked:
  - `claude plugin marketplace add anthropics/claude-plugins-official`
  - `claude plugin install linear@claude-plugins-official --scope user`
  - `claude plugin install context7@claude-plugins-official --scope user`
  - `claude plugin uninstall linear@claude-plugins-official --scope user`

  After the installs, `claude mcp list` showed `plugin:linear:linear` and `plugin:context7:context7` as
  "Needs authentication".
- The marketplace was cloned over SSH (`git@github.com:…`) on a Mac that has GitHub SSH keys.
- `claude plugin install` has `--json`, `--config key=value` (plugin options) and `--accept-command <sha256>`
  (plugins whose source is a command need a person to see and accept that command).
- The Choose models page (`internal/picker`) already has a one-time sign-in link, a session cookie on `/picker`,
  `Origin` and `X-Second-Life-Picker` checks, a strict CSP, and `api/<name>` routes in a method table.
- `internal/mcpimport` already runs `claude` safely: found with `codex.FindTool`, started in the home folder with
  `codex.ToolEnv`, no standard input, capped output, and killed when the context ends.

## Approaches

| | Approach | Trade-off |
|---|---|---|
| A (chosen) | The page drives the `claude plugin` commands | Claude Code downloads, checks and updates plugins. Covers Claude Code and the Desktop Code tab. Needs Claude Code installed. |
| B | A, plus writing Desktop's marketplace list and `managedMcpServers` | Also covers Chat and Cowork. About twice the work: Desktop shows only the 55 plugins whose files are inside Anthropic's repository, and needs a separate sign-in and a restart. |
| C | Second Life downloads and places plugin files itself | Rejected: a copy of Claude Code with its own bugs and no updates. |

The owner chose A on 2026-11-26. B can be added later without changing A.

## Design

### Where the page lives

- It is a second page of the existing picker: `/picker/plugins`, with its own `plugins.js`, sharing `app.css`, the
  sign-in and the cookie.
- The models page and the plugins page link to each other in the header.
- The tray menu gets "Plugins..." under "Import MCP servers..." (both add tools to Claude; the items under "Choose
  models..." are pinned as one group by `view_test.go`). It opens a one-time link to the plugins page and is enabled
  under the same conditions as "Choose models...".
- The sign-in code stays in the URL fragment as today.
- Choice (builder may change): how the link names the page, either `/picker/plugins#code` or one entry page that
  routes.

### What the page shows

- A search box over name and description.
- A filter, "Anthropic" (the 316 of `claude-plugins-official`, default) or "All" (adds the 3,783 of the Directory).
  The Directory also lists plugins that Anthropic did not write, so the "All" view says that.
- One row per plugin:
  - name
  - description, cut to 300 characters
  - marketplace
  - install count when present
  - an Install or Remove button
- Installed plugins come first under "Installed".
- Opening a row shows what it contains, from `claude plugin details <id>`: skills, agents, hooks, MCP servers and the
  token cost. This is fetched only when opened.
- An empty catalog has a "Get the catalog" button, which adds `anthropics/claude-plugins-official` when Claude Code
  does not have it.
- An "Update catalog" button runs `claude plugin marketplace update`.

### Installing

- Install opens a confirmation:
  - the plugin's name and marketplace
  - "It runs on this computer with your permissions"
  - what it contains, when known
- On yes the server runs `claude plugin install <id> --scope user --json`. Only one plugin command runs at a time; the
  others wait or are refused with "busy".
- On success the page reloads the installed list and shows the next step:
  - MCP servers: "Open Claude (menu: Open Claude), type /mcp and sign in to <server>."
  - Skills only: "It is ready in new Claude sessions."
- A plugin that needs something the page does not do is not installed. The row says what to run in a terminal and
  why. This covers:
  - an accepted command (the `--accept-command` case)
  - required options (`--config`)
- Remove asks once, then runs `claude plugin uninstall <id> --scope <its scope> --keep-data` (see Decisions).
- Errors show Claude Code's message, cleaned and cut like the rest of the picker's errors.
- The page never shows a key or token. Claude Code's output can name paths; that is fine on the owner's own page.

### Server side

- A new `Deps` part, implemented in `internal/bridge` and backed by a small runner like `mcpimport.Machine`:
  - `Plugins(ctx) (installed, available, err)`
  - `PluginDetails(ctx, id)`
  - `InstallPlugin(ctx, id)`
  - `RemovePlugin(ctx, id)`
  - `AddCatalog(ctx)`
  - `UpdateCatalog(ctx)`
- New routes in the picker's method table:

  | Route | Methods |
  |---|---|
  | `plugins` | GET |
  | `plugin` | POST install, DELETE remove |
  | `plugin-details` | POST |
  | `catalog` | POST add, PUT update |

- A plugin id is checked against a strict pattern (`name@marketplace`, letters, digits, `._-`). An install id must be in `available`, a remove id in `installed`, before it reaches a command. Arguments go to `exec`, never through a shell.
- Time limits: listing 30 s, details 30 s, install and remove 3 min, catalog add 3 min.
- Text from the catalog is untrusted, as model text is today: it is shown as text, cut, and never as HTML.
- When `claude` is missing, every plugin call answers with one clear state ("Claude Code is not installed").
  The page then shows the install command for the system (`curl -fsSL https://claude.ai/install.sh | bash`, or the PowerShell one on
  Windows) and the doctor hint.
- If the user set `CLAUDE_CONFIG_DIR`, the commands use it, as Claude Code itself would.

### Decisions after the builder check (author's calls, 2026-11-26)

| Question | Decision |
|---|---|
| Does `claude plugin details` work for a plugin that is not installed? | Checked 2026-11-26: no, it answers `Plugin "…" not found` and downloads nothing. So rows of plugins that are not installed show catalog fields only, the confirmation says "what it contains is shown after install", and details are offered for installed plugins only. Original plan, kept for the record: Not checked. The first build step checks it in a temporary `CLAUDE_CONFIG_DIR`. If it works without installing, the row and the confirmation show its text as it is, unparsed, in a `<pre>`. If it does not, they show the catalog fields only, and the confirmation says "what it contains is shown after install". |
| Does Remove delete the plugin's data? | No. Remove runs `uninstall <id> --scope <its scope> --keep-data`. Deleting data cannot be undone and is left to the terminal. |
| Dependencies | The confirmation lists the dependencies that would come with the plugin, when Claude Code reports them (from `--json` or the catalog entry). Remove does not prune them. If they cannot be known before installing, the confirmation says "it may install plugins it depends on". |
| Plugins installed elsewhere | "Installed" shows every installed plugin from `installed`, at any scope and from any marketplace. Remove is allowed for any id in `installed`, with that entry's scope. Install is allowed only for ids in `available`. Plugins loaded only for a session (`--plugin-dir`, no scope) are shown without a button. |
| Windows | The code is written for both systems, as the rest of the app is. The missing-Claude hint shows `irm https://claude.ai/install.ps1 \| iex` on Windows. Tests with the fake `claude` run on Unix; Windows is checked by building and by the existing CI. No Windows release is part of this work. |
| `CLAUDE_CONFIG_DIR` | The app uses its own process environment, as `mcpimport` does. The page shows the folder it works on ("Plugins in ~/.claude"), so a person whose terminal uses another folder can see the mismatch. |
| Oldest Claude Code version | No version number is pinned. If `claude plugin list --available --json` fails or prints something else, the page says "Update Claude Code" with the update command and stops there. |
| When "Get the catalog" appears | When `claude-plugins-official` is not among the marketplaces, whatever the Directory holds. The default "Anthropic" view then shows that button in place of the empty list. |

## Verify

- Unit tests with a fake `claude`, the way `cmd/slife/mcp_unix_test.go` does it:
  - parsing the JSON
  - id checks
  - refusal of ids outside the catalog
  - one command at a time
  - time limits
  - missing claude
  - errors cleaned
- Handler tests in `internal/picker`:
  - the sign-in and header rules apply to the new routes
  - methods
  - bodies
- A live check against a temporary `CLAUDE_CONFIG_DIR`: install and remove `context7@claude-plugins-official` from
  the page, then `claude mcp list` shows it and then does not.
- A browser check of the page:
  - search
  - install confirmation
  - next-step text
  - dark mode
  - narrow window
  - keyboard only
- `go test -p 1 ./...` (tests run one process at a time on this machine).

## Left to the builder

- How the plugins page is laid out within the existing look (it must match the models page).
- Whether the catalog is cached in memory between page loads, and for how long.
- How "install needs a command or options" is detected: from `--json` output, from `source.source == "command"` and
  the plugin manifest, or from a first refused run.
- The exact link scheme for the plugins page.
- Whether rows are paged or drawn on scroll for 4,099 entries.

## Risks

- Cloning over SSH. On a Mac without GitHub SSH keys, `marketplace add` may need HTTPS. Check on a clean account
  before release, and pass an HTTPS URL if needed.
- The JSON of `claude plugin list --available --json` is not a documented contract. Parse it loosely and fail with a
  clear message if it changes.
- A plugin from the Directory can bundle hooks or local servers that run code. The confirmation says so; we do not
  vet plugins.
