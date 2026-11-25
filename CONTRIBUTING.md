# Contributing

Bugs and ideas go to [issues](https://github.com/Lookoff-AIMLAPI/second-life/issues/new/choose). Before a large change, open an issue first, so you do not write something that will not be merged.

## Build

You need Go (the version is in `go.mod`). The app itself needs a Mac with the Command Line Tools.

```sh
make build           # dist/slife
make tray            # dist/slife-tray
make mac-universal   # the app, the DMG, the zip and install.sh
```

A build of yours uses the same port and the same `~/.second-life` as the released app. Quit that one first, or set `SLIFE_HOME` and `SLIFE_ADDR`.

## Test

```sh
make check
go test -race -p 1 ./...
shellcheck packaging/mac/*.sh
```

`go test -tags live -run Live ./internal/bridge` calls real models with your Codex sign-in and spends your limits. The usual tests never do.

## Pull requests

- Branch from `trunk` and open the pull request into `trunk`. CI runs `make check` and a Windows build.
- One change per pull request, with a test that fails without it.
- No keys, captured requests or answers in code, tests or descriptions.
- Do not rename the signature prefixes `c2c1.`, `c2r1.` and `c2x1_`. They are in people's saved conversations.
- New dependencies need a reason in the description.

Your change is released under the [MIT license](LICENSE).
