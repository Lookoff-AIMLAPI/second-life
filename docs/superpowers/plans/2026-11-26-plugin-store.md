# Plugin Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A browser page in Second Life where a person installs and removes Claude Code plugins from Anthropic's
catalog with one click.

**Architecture:** A new package `internal/plugins` runs the `claude plugin …` commands and parses their JSON. The
picker (`internal/picker`) gets a second page, `/picker/plugins`, and four API routes that call it through `Deps`.
The bridge wires the runner in, and the tray gets a "Plugins..." item that opens a one-time link.

**Tech Stack:** Go 1.27 (stdlib `net/http`, `os/exec`), embedded static HTML/CSS/JS (no build step, no libraries),
systray.

**Spec:** `docs/superpowers/specs/2026-11-26-plugin-store-design.md` (approved 2026-11-26). Read it first; the
"Decisions after the builder check" table overrides the text above it.

## Global Constraints

- Second Life never downloads, unpacks or writes plugin files; only `claude plugin …` commands do.
- Every `claude` run goes through `codex.Command`, `Dir` = home folder, `Env` = `codex.ToolEnv(bin)`, no stdin,
  output capped at 4 MiB, killed when the context ends: reuse the pattern of `internal/mcpimport/machine.go:start`.
- Install: `claude plugin install <id> --scope user --json`. Remove: `claude plugin uninstall <id> --scope <scope> --keep-data`.
- Time limits: catalog 30 s, details 30 s, install and remove 3 min, catalog add and update 3 min.
- One plugin command at a time. A second one is refused with HTTP 409 and the text `another plugin change is running`.
- Install ids must be in `available`, remove ids in `installed`. Id pattern: `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}@[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`.
- Catalog text is untrusted: through `text()` (name 100 bytes, description 300) on the server, `textContent` on the
  page, never HTML.
- Official marketplace id: `claude-plugins-official`, repo `anthropics/claude-plugins-official`. Directory id:
  `anthropic-plugin-directory`.
- Install hints:
  - Unix: `curl -fsSL https://claude.ai/install.sh | bash`
  - Windows: `irm https://claude.ai/install.ps1 | iex`
- Page copy is plain English, the voice of the existing models page (`static/app.js`).
- Tests run one process at a time: `go test -p 1 …`.
- Commits go to the local branch `plugin-store`. Nothing is pushed. No `Co-Authored-By` or "Generated with" lines.

## Review Focus

1. **The whole catalog in one list (4,099 rows).** Typing in search must not freeze the page. Draw at most 200 rows,
   then show "Show 200 more"; covered in Task 4 Step 5.
2. **Two clicks or two tabs installing at once.** The second request gets 409 and the page says another change is
   running; covered in Task 1 Step 1 (`TestBusy`) and Task 2 (`TestPluginBusy`).
3. **Claude Code prints a warning line before its JSON, or prints to stderr.** Parsing must use stdout and find the
   JSON object, not fail on line one; covered in Task 1 (`TestCatalogWithNoise`).
4. **A slow clone during install.** The button shows "Installing…" and stays disabled, and the server stops at
   3 minutes with a readable error; covered in Task 1 (`TestTimeout`), Task 4 Step 5.
5. **Catalog text with markup or control characters.** The text is shown literally, without breaking the page;
   covered in Task 2 (`TestPluginTextIsCleaned`).

---

### Task 1: `internal/plugins` runner

**Files:**
- Create: `internal/plugins/plugins.go`
- Create: `internal/plugins/run.go` (the `exec` part, copied in shape from `mcpimport.start`)
- Test: `internal/plugins/plugins_unix_test.go` (`//go:build !windows`; the test binary acts as fake `claude` when
  `SLIFE_FAKE_CLAUDE` is set, as `cmd/slife/mcp_unix_test.go` does)
- Test: `internal/plugins/plugins_test.go` (pure parsing and id tests, all systems)

**Interfaces:**
- Produces:

```go
var (
    ErrNoClaude  = errors.New("Claude Code is not installed")
    ErrOldClaude = errors.New("this Claude Code cannot list plugins; update it")
    ErrBusy      = errors.New("another plugin change is running")
    ErrUnknown   = errors.New("no such plugin")
)
type Plugin struct {
    ID, Name, Description, Marketplace, SourceKind string
    Installs int // 0 when the catalog has no count
}
type Installed struct {
    ID, Version, Scope string // Scope "" for session-only plugins
    Enabled    bool
    MCPServers []string // sorted names
}
type Catalog struct {
    Installed   []Installed
    Available   []Plugin
    HasOfficial bool   // some Available entry has Marketplace "claude-plugins-official"
    ConfigDir   string // $CLAUDE_CONFIG_DIR if absolute, else ~/.claude
}
type Result struct {
    MCPServers    []string // of the installed plugin, from the catalog re-read after install
    NeedsTerminal string   // non-empty: not installed; the command to run and why
}
type Runner struct {
    GOOS  string
    Find  func(name string) (string, error)
    Run   func(ctx context.Context, bin string, args ...string) (stdout []byte, err error)
    Waits struct{ List, Details, Change time.Duration } // 30s, 30s, 3m; tests shorten them
}
func Here() *Runner
func ValidID(id string) bool
func (r *Runner) Catalog(ctx context.Context) (Catalog, error)
func (r *Runner) Details(ctx context.Context, id string) (string, error)
func (r *Runner) Install(ctx context.Context, id string) (Result, error)
func (r *Runner) Remove(ctx context.Context, id string) error // scope taken from Catalog().Installed
func (r *Runner) AddCatalog(ctx context.Context) error        // marketplace add anthropics/claude-plugins-official
func (r *Runner) UpdateCatalog(ctx context.Context) error     // marketplace update claude-plugins-official
```

- [ ] **Step 1: Find out what `claude plugin details` does for a plugin that is not installed**

  Run this in a throwaway config and write the answer into the spec's Decisions table. It needs network and the
  owner's permission prompt.
  - Command:

    ```bash
    D=$(mktemp -d); CLAUDE_CONFIG_DIR=$D claude plugin marketplace add anthropics/claude-plugins-official; CLAUDE_CONFIG_DIR=$D claude plugin details linear@claude-plugins-official; ls $D/plugins/cache; rm -rf $D
    ```

  - Expected: either a component inventory with `plugins/cache` still empty, or an error/download.
  - If it downloads or fails, `Details` returns `ErrUnknown` for ids not in `Installed` and the page uses the
    fallback text from the spec.

- [ ] **Step 2: Write the failing tests**
  - In `plugins_test.go`:
    - `TestValidID`. Accepts `linear@claude-plugins-official`. Rejects `""`, `a`, `-x@y`, `a@b@c`, `a b@c`,
      `a@b;rm`, and a 129-character name.
    - `TestParseCatalog`. Feed the JSON shape from the spec's Facts section: one installed entry with `mcpServers`
      `{"context7":{…}}`, one official and one directory entry.
      - Assert `HasOfficial == true` and `Installed[0].MCPServers == []string{"context7"}`.
      - Assert `Available[1].Marketplace == "anthropic-plugin-directory"` and `SourceKind == "git-subdir"`.
      - Assert `Installs` reads `installCount`.
    - `TestCatalogWithNoise`. The same JSON preceded by `Warning: something\n` parses.
    - `TestParseCatalogChanged`. `{"plugins":[]}` returns `ErrOldClaude`.
  - In `plugins_unix_test.go` (fake `claude` logs its args as a JSON line and prints canned stdout from
    `SLIFE_FAKE_OUT`, exits with `SLIFE_FAKE_EXIT`, sleeps `SLIFE_FAKE_SLEEP`):
    - `TestInstallArgs`. Expects args `["plugin","install","linear@claude-plugins-official","--scope","user","--json"]`,
      then a catalog re-read.
    - `TestRemoveUsesItsScope`. Installed at scope `project`: expects
      `["plugin","uninstall","x@m","--scope","project","--keep-data"]`.
    - `TestRefusesUnknownIDs`. Install of an id not in `available`, and remove of one not in `installed`, return
      `ErrUnknown` with no `install`/`uninstall` line logged.
    - `TestSessionOnlyNotRemovable`. Remove of an installed entry with empty scope returns `ErrUnknown`.
    - `TestCommandSourceNeedsTerminal`. An available entry with `source.source == "command"`: `Install` returns
      `Result.NeedsTerminal` containing `claude plugin install <id>`, nothing installed.
    - `TestBusy`. Start an install with `SLIFE_FAKE_SLEEP=2s`. A second `Install` meanwhile returns `ErrBusy`.
    - `TestTimeout`. `Waits.Change = 200ms`, fake sleeps 2 s. `Install` returns an error mentioning `took too long`
      within 3 s.
    - `TestNoClaude`. `Find` returns `exec.ErrNotFound`, so every method returns `ErrNoClaude`.
    - `TestConfigDir`. `CLAUDE_CONFIG_DIR=/x/y` gives `ConfigDir == "/x/y"`. A relative value gives `~/.claude`.

- [ ] **Step 3: Run the tests and check that they fail**

  Run: `go test -p 1 ./internal/plugins/`
  Expected: FAIL, package does not compile.

- [ ] **Step 4: Implement `plugins.go` and `run.go` to the Interfaces above**

  - Parsing: decode from the first `{` of stdout into a struct with `installed`/`available` arrays. If both arrays are
    missing, return `ErrOldClaude`. A non-zero exit of `list` whose output contains `unknown option` also returns
    `ErrOldClaude`.
  - `Install`:
    1. read the catalog
    2. check the id
    3. refuse `SourceKind == "command"`
    4. run the command
    5. on exit ≠ 0 with output that mentions `--config` or `userConfig`, return `NeedsTerminal`
       `"It needs settings first. In Terminal: claude plugin install <id>"`
    6. re-read the catalog for `MCPServers`.
  - Busy: `sync.Mutex` `TryLock` around install, remove, add and update. Catalog and details do not lock.
  - Errors carry Claude Code's last 300 bytes of output, without the binary's path.

- [ ] **Step 5: Run the tests and check that they pass**

  Run: `go test -p 1 ./internal/plugins/ && go vet ./internal/plugins/`
  Expected: `ok`.

- [ ] **Step 6: Commit**

  ```bash
  git add internal/plugins docs/superpowers/specs/2026-11-26-plugin-store-design.md
  git commit -m "plugins: run Claude Code's plugin commands"
  ```

### Task 2: picker API routes

**Files:**
- Create: `internal/picker/plugins.go` (handlers and wire types)
- Modify: `internal/picker/picker.go` (`Deps` gets one method; four routes in `New`; `pages` gets the plugins page in
  Task 4)
- Test: `internal/picker/plugins_test.go`; `internal/picker/picker_test.go` (`fake` gets `Plugins()`)

**Interfaces:**
- Consumes: Task 1 types and errors.
- Produces:

```go
// in Deps:
Plugins() PluginStore // nil: the store is not available in this build
type PluginStore interface {
    Catalog(ctx context.Context) (plugins.Catalog, error)
    Details(ctx context.Context, id string) (string, error)
    Install(ctx context.Context, id string) (plugins.Result, error)
    Remove(ctx context.Context, id string) error
    AddCatalog(ctx context.Context) error
    UpdateCatalog(ctx context.Context) error
}
```

- Routes and wire JSON (field names fixed; the page in Task 4 depends on them):

| Route | Request | 200 response |
|---|---|---|
| `GET plugins` | — | `{"state":"ok"\|"no-claude"\|"old-claude","installHint":string,"configDir":string,"hasOfficial":bool,"installed":[{"id","version","scope","enabled","mcpServers":[]}],"available":[{"id","name","description","marketplace","installs","needsTerminal":bool}]}` |
| `POST plugin` | `{"id"}` | `{"ok":true,"servers":[...]}` or `{"ok":false,"needsTerminal":string}` |
| `DELETE plugin` | `{"id"}` | `{"ok":true}` |
| `POST plugin-details` | `{"id"}` | `{"text":string}` (cleaned, ≤ 4000 bytes) or `{"text":""}` when unknown |
| `POST catalog` / `PUT catalog` | `{}` | `{"ok":true}` |

  - `ErrBusy` → 409.
  - `ErrUnknown` → 404 `no such plugin`.
  - Other errors → 502 with `clean(err, 300)`.
  - `ErrNoClaude` and `ErrOldClaude` on `GET plugins` → 200 with that `state`. On other routes → 503.
  - `installHint` is the Global Constraints line for `runtime.GOOS`.

- [ ] **Step 1: Write the failing tests** in `plugins_test.go`, using a `fakeStore` field on `fake`:
  - The existing `TestEveryCallIsGuarded` and `TestMethodsAndPaths` must cover the new routes; add them to their
    tables if they list routes by hand.
  - `TestPluginRoutesNeedSignIn`. Each of the six method+route pairs without the cookie returns 401. Without
    `X-Second-Life-Picker: 1` it returns 403.
  - `TestPluginList`. The JSON keys match the table. `needsTerminal` is true for `SourceKind == "command"`.
  - `TestPluginTextIsCleaned`. A description `"<b>x</b>‮\u0007" + 400×"a"` comes back with no `‮` or
    `\u0007` and ≤ 300 bytes; the `<b>` stays literal.
  - `TestPluginBusy`. The store returns `ErrBusy`, so install answers 409 with `{"error":"another plugin change is running"}`.
  - `TestPluginNoClaude`. `GET plugins` answers 200 with `"state":"no-claude"` and a non-empty `installHint`.
  - `TestPluginBadID`. `{"id":"a b"}` answers 400 and the store is not called.
  - `TestPluginsNil`. `Plugins()` returns nil, so `GET plugins` answers 503.

- [ ] **Step 2: Run them and check that they fail.** Run `go test -p 1 ./internal/picker/`; expect FAIL.
- [ ] **Step 3: Implement `plugins.go`.** Validate ids with `plugins.ValidID` before calling the store. Contexts get
  the Task 1 limits, plus 5 s so the runner's own timeout reports first.
- [ ] **Step 4: Run and check that they pass.** Run `go test -p 1 ./internal/picker/ && go vet ./internal/picker/`;
  expect `ok`.
- [ ] **Step 5: Commit**

  ```bash
  git add internal/picker
  git commit -m "picker: plugin store API"
  ```

### Task 3: bridge wiring and the one-time link

**Files:**
- Modify: `internal/bridge/pick.go` (`func (d pickerDeps) Plugins() picker.PluginStore`, returns the bridge's
  runner)
- Modify: `internal/bridge/bridge.go:145` area (create `plugins.Here()` once and keep it on the bridge)
- Modify: `internal/bridge/aiml.go:402` (add `PluginsURL()` next to `PickerURL()`)
- Modify: `internal/picker/picker.go` (`func (p *Picker) PageURL(base, page string) string`; `URL` becomes
  `PageURL(base, "")`)
- Test: `internal/picker/picker_test.go`, `internal/bridge/bridge_test.go`

**Interfaces:**
- Produces:
  - `func (b *Bridge) PluginsURL() string`: empty while off; otherwise `http://<addr>/picker/plugins#<code>`.
  - `func (p *Picker) PageURL(base, page string) string`.

- [ ] **Step 1: Write the failing tests**
  - `TestPageURL`: `PageURL("http://h", "plugins")` matches `^http://h/picker/plugins#[0-9a-f]{64}$`, and the code
    signs in once through `POST api/session`.
  - In the bridge: `TestPluginsURLOff`, empty while the state is `StateOff`.
- [ ] **Step 2: Run them and check that they fail.** `go test -p 1 ./internal/picker/ ./internal/bridge/`
- [ ] **Step 3: Implement the three changes above.**
- [ ] **Step 4: Run and check that they pass.** Same command; expect `ok`.
- [ ] **Step 5: Commit** with `git commit -m "bridge: plugin store link and runner"`.

### Task 4: the plugins page

**Files:**
- Create: `internal/picker/static/plugins.html`, `internal/picker/static/plugins.js`
- Create: `internal/picker/static/common.js`. It holds `$`, `h`, `call` and `signIn`, moved out of `app.js`
  unchanged. `app.js` and `plugins.js` both use it, and `index.html` loads it before `app.js`.
- Modify: `internal/picker/static/app.css` (rows, filter, details `<pre>`), `internal/picker/static/index.html`
  (header link "Plugins"), `internal/picker/picker.go` `pages` map:

  | Path | File |
  |---|---|
  | `prefix+"plugins"` | `plugins.html` |
  | `prefix+"plugins.js"` | `plugins.js` |
  | `prefix+"common.js"` | `common.js` |

- Test: `internal/picker/picker_test.go` (`TestPageHeaders` and `TestAssetsStayLocal` get the three paths)

**Interfaces:**
- Consumes: Task 2 routes and JSON.

- [ ] **Step 1: Write the failing test.** Extend `TestPageHeaders` to expect 200 with `text/html` for `/picker/plugins`, and
  `text/javascript` for `/picker/plugins.js` and `/picker/common.js`, each with the same CSP header as
  `/picker/`.
- [ ] **Step 2: Run it and check that it fails.** `go test -p 1 ./internal/picker/ -run 'TestPageHeaders|TestAssetsStayLocal'`
- [ ] **Step 3: Move the shared helpers into `common.js`.** Change nothing in their behaviour, then reload the
  models page in the browser: sign-in, model list and Save still work.
- [ ] **Step 4: Write `plugins.html` and `plugins.js`.**
  - Page parts and copy:

    | Part | Content |
    |---|---|
    | Title | "Plugins · Second Life" |
    | `h1` | "Add tools to Claude" |
    | Lead | "Plugins add skills and connections to services. They are installed for Claude Code and the Code tab of Claude Desktop." |
    | Line under the header | "Plugins in `<configDir>`" |
    | Header link | back to "Models" |
    | Filter | radio "Anthropic" (default) / "All". "All" adds the line: "These include plugins that Anthropic did not write." |
    | Search | one text input over name and description |
    | Sections | "Installed" first, then the catalog |

  - Install confirmation (the existing `<dialog>` pattern of `ask()` in `app.js`):
    - text: `"<name> from <marketplace> runs on this computer with your permissions. It may also install plugins it depends on."`
    - plus the details text when Task 1 Step 1 showed details work without installing, otherwise
      `"What it contains is shown after install."`
    - button "Install"
  - After an install:

    | Result | Message |
    |---|---|
    | servers | "Installed. To sign in to <servers>: open Claude from the menu, type /mcp and choose each one." |
    | no servers | "Installed. It is ready in new Claude sessions." |
    | `needsTerminal` | the text as a notice, no install |

  - Remove: a confirmation "Remove <name>? Its saved data stays.", then the Installed list is reloaded.
  - States:

    | State | What the page shows |
    |---|---|
    | `no-claude` | "Claude Code is not installed." plus `installHint` in a copyable `<code>` |
    | `old-claude` | "Update Claude Code to use plugins." |
    | `!hasOfficial` with the Anthropic filter | a "Get the catalog" button (POST `catalog`) in place of the list |
    | always | an "Update catalog" button (PUT `catalog`) |

- [ ] **Step 5: Handle the Review Focus cases in `plugins.js`.**
  - Draw at most 200 rows and add a "Show 200 more" button. Search filters the whole array and redraws at most
    200.
  - While a change runs, every Install/Remove button is disabled and the clicked one reads "Installing…" or
    "Removing…".
  - A 409 shows "Another plugin change is running. Wait for it to finish."
  - All catalog text goes in with `textContent`.
- [ ] **Step 6: Run the tests.** `go test -p 1 ./internal/picker/`; expect `ok`.
- [ ] **Step 7: Check in the browser.** `go run ./internal/picker/dev` serves the page with fake deps; extend that
  dev main with a fake store of 4,099 entries. Check:
  - search
  - "Show 200 more"
  - the install dialog
  - the next-step text
  - the 409 text
  - dark mode
  - a 375 px wide window
  - keyboard only (Tab to the search, the filter, Install, the dialog buttons)

  Take a screenshot of each state.
- [ ] **Step 8: Commit** with `git commit -m "picker: plugins page"`.

### Task 5: tray menu item

**Files:**
- Modify: `internal/tray/view.go` (`itemPlugins` after `itemMCP`; title "Plugins...", shown always, enabled like
  `itemPick`)
- Modify: `internal/tray/tray.go` (controller gets `PluginsURL() string`; `on(itemPlugins, a.plugins)`)
- Modify: `internal/tray/app.go` (`func (a *app) plugins() bool`, the same as `pick()` with `PluginsURL`)
- Test: `internal/tray/view_test.go`, `internal/tray/app_test.go` (`fakeController.PluginsURL` returns
  `"http://127.0.0.1:8787/picker/plugins#x"`)

- [ ] **Step 1: Write the failing tests.**
  - `TestPluginsItem`: "Plugins..." comes right after "Import MCP servers...", and is enabled when on and disabled
    when off.
  - In `app_test.go`: clicking it opens the URL through `openLocal`.
- [ ] **Step 2: Run them and check that they fail.** `go test -p 1 ./internal/tray/` (needs the desktop toolkit; on
  macOS it builds).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and check that they pass.** Same command; expect `ok`.
- [ ] **Step 5: Commit** with `git commit -m "tray: Plugins menu item"`.

### Task 6: live check and README

**Files:**
- Modify: `README.md` ("Use" section: one item for Plugins; commands table unchanged)

- [ ] **Step 1: Run the full suite.**

  ```bash
  gofmt -l . && go vet ./... && go test -p 1 ./...
  ```

  Expected: no gofmt output, vet clean, all `ok`.
- [ ] **Step 2: Do the live check against a temporary Claude config.**
  - Build the tray with `make tray`.
  - Start it with `CLAUDE_CONFIG_DIR=$(mktemp -d)`.
  - Open "Plugins...". Press "Get the catalog", install `context7@claude-plugins-official`, then remove it.
  - After the install, `CLAUDE_CONFIG_DIR=<same> claude mcp list` shows `plugin:context7:context7`. After the
    removal it does not.
  - Delete the folder.
- [ ] **Step 3: Write the README item.**

  > Plugins opens a page where you add skills and connections from Anthropic's catalog to Claude Code. After an
  > install, type /mcp in Claude to sign in to the services it adds.

- [ ] **Step 4: Commit** with `git commit -m "README: Plugins"`.
