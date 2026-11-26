# Second Life

Run Claude Code and Claude Desktop on models you pick. A menu bar app for the Mac keeps a small server on your own computer. Claude talks to it as usual, and it passes each request to:

- Codex models, through your ChatGPT sign-in and its limits
- AIMLAPI models, through your AIMLAPI key and balance
- any server that speaks OpenAI Chat Completions, OpenAI Responses or Anthropic Messages, such as OpenRouter, Ollama, LM Studio or vLLM

You set this per Claude name. Opus can be one model and Haiku another.

| Claude name | Model after install |
|---|---|
| Fable | `gpt-6-astra` |
| Opus | `gpt-5.6-sol` |
| Sonnet | `gpt-5.6-terra` |
| Haiku | `gpt-5.6-luna` |

Plain `claude` and the usual Claude Desktop mode stay as they were.

## Install

macOS 13 or newer, Intel or Apple Silicon. Open Terminal (Cmd+Space, type Terminal, Enter) and paste:

```sh
curl -fsSL https://github.com/Lookoff-AIMLAPI/second-life/releases/latest/download/install.sh | sh
```

This downloads the script of the latest tagged release and runs it. The script names its own release, downloads the DMG of that release, compares its SHA-256 and code signature, and copies the app to Applications. It asks before each step that changes something. It never asks for a password. If something asks you for one, stop.

To read the script before it runs:

```sh
curl -fsSLO https://github.com/Lookoff-AIMLAPI/second-life/releases/latest/download/install.sh
less install.sh
sh install.sh
```

A certain version instead of the latest. Put its tag in place of `vX.Y.Z`:

```sh
curl -fsSL https://github.com/Lookoff-AIMLAPI/second-life/releases/download/vX.Y.Z/install.sh | sh
```

With the GitHub CLI:

```sh
gh release download --repo Lookoff-AIMLAPI/second-life --pattern install.sh
sh install.sh
```

Somebody sent you the zip? Unpack it, type `sh ` in Terminal with a space at the end, drag `install.sh` from the folder into the window and press Enter.

To update, run the install command again. Your keys and settings stay.

### Why a script

The app is signed on the build machine but not notarized by Apple, which needs a paid account. macOS marks files that a browser or a messenger saves and refuses to start such an app. Files that `curl` saves carry no mark. For a DMG you got another way, the script explains the mark and asks before it clears it. Say yes only if you trust the source.

### What you need

| For | You need |
|---|---|
| Codex models | The Codex CLI and a ChatGPT plan. The installer and the menu offer OpenAI's own setup script |
| AIMLAPI models | An AIMLAPI key |
| Your own server | Its address, and a key if it has one |
| Claude in Terminal | Claude Code |
| Claude with windows | Claude Desktop |

One source of models is enough. Without Codex, put all four names on the others: Claude uses Haiku for its own small requests.

## Use

The app has no window. Look for its icon in the menu bar, which says `On: 127.0.0.1:8787`.

1. Choose models opens a page in your browser. Add keys and servers, pick a model for each name, press Test, then Save.
2. Open Claude starts Claude Code in Terminal.
3. Set up Claude Desktop switches it to this app. A window lists the changes first, and the old settings are backed up. Claude restarts. Switch Claude back to claude.ai returns to the usual mode.
4. Plugins opens a page where you add skills and connections from Anthropic's catalog to Claude Code. After an install, type /mcp in Claude to sign in to the services it adds.

Your usual chats are in Claude under Settings > Import & export > Import. The import copies the list once. Code chats share one transcript, so deleting an imported chat can delete the original.

Import MCP servers shows every server from Codex and your usual Claude and asks before adding each one to Claude Code. Set up Claude Desktop copies the local ones and names them first. Their keys are then also in the backups under `~/.second-life/backups`, readable only by you. Sign-ins of remote servers cannot be copied.

### Your own servers

Choose models > Your endpoints > Add. Enter a name, the address up to the version (`https://host/v1`), the protocol and a key if the server needs one. Check lists its models. Plain http works only for this computer. A key belongs to one address: change the address and you enter the key again.

Prompts, files and tool output go to that server. When it refuses a field, the app sends the request again without the field and remembers that.

### API

Address `http://127.0.0.1:8787`, protocol Anthropic Messages. The menu copies the address and the key. Use `opus`, `sonnet`, `fable`, `haiku`, full Claude ids, or the extra models you added. The key works only for this app, but it spends your limits and balance.

### Commands

They are in the app: `/Applications/Second Life.app/Contents/MacOS/slife`

| Command | What it does |
|---|---|
| `slife doctor` | Shows what is installed and what is missing |
| `slife codex` | Installs the Codex CLI and signs in |
| `slife claude [args]` | Starts Claude Code on its own private server |
| `slife mcp list`, `slife mcp import` | Lists and imports MCP servers |
| `slife status`, `slife logs -f` | Shows the state and the log |
| `slife serve` | Runs the server without the app |

## When something is wrong

| You see | Do this |
|---|---|
| "The application can't be opened" | Use the install script. For a copy you trust: `xattr -dr com.apple.quarantine "/Applications/Second Life.app"` |
| `open` shows nothing | Look in the menu bar. If you started the app from the DMG, quit that copy first |
| `Sign in needed` | Sign in with ChatGPT in the menu, or `codex login` |
| `The usage limit has been reached` | Your ChatGPT limit. Put some names on other models |
| 401 about the Codex CLI | That name is on Codex. Pick another model for it |
| "Your inference configuration blocks this website" | Run Set up Claude Desktop again, or use the Code tab |
| Slow answers | `slife logs`. `text_ms` is the wait for the first text, and `slow` lines say what it waits for |
| Anything else | `slife doctor`. The report and the log hold no keys and no text of yours |

## Limits

- Codex models hold 272k tokens and Claude assumes more, so at 240k the app asks Claude to compact the conversation. For smaller models it asks earlier.
- Codex ignores `max_tokens`, `temperature`, `top_p` and `top_k`. The app cuts text at `stop_sequences` itself.
- Server tools run by Anthropic are refused, except web search on Codex models.
- The Chat Completions path has run against test servers only. If yours fails, open an issue with the error text.
- There is no Windows release. The code compiles for Windows, but nobody has run it there.
- Using the Codex service outside OpenAI's own client is unofficial. Check the terms of the services you connect.

## Safety

The server listens on this computer only and needs its own key. The Codex CLI keeps and renews the ChatGPT sign-in; this app never reads the renewal token. Keys for AIMLAPI and your servers are in the Keychain. The log holds sizes and times.

Set up Claude Desktop lets Claude's tools open every website in that mode, because without it Chat and Cowork open none. The window says so, and Claude keeps the list you set under Allowed egress hosts.

## Build

Go 1.27. `make check` runs the tests. `UNIVERSAL=1 VERSION=X.Y.Z packaging/mac/build.sh` makes the app, the DMG, the zip and `install.sh` in `dist`. `go test -tags live ./internal/bridge` calls real models and spends your limits.

## Help

Bugs and ideas: [issues](https://github.com/Lookoff-AIMLAPI/second-life/issues/new/choose). Security problems: [SECURITY.md](SECURITY.md). Changes to the code: [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE). [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) lists the parts by others.

Second Life here is an independent open-source project. It is not affiliated with Linden Lab, OpenAI, Anthropic or AIMLAPI.
