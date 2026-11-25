# Security

## Report a problem

Use [a private report](https://github.com/Lookoff-AIMLAPI/second-life/security/advisories/new). Only the maintainers see it. Do not open an issue, and leave out real keys and tokens.

Write the version (`slife version`), the steps, and what an attacker gets.

Fixes go into the next release. Older releases get none.

## What matters here

- Reading a key: the Keychain entries, the local API key, the ChatGPT sign-in, keys in the backups under `~/.second-life/backups`
- Using the local server without its key, from a web page or from another computer
- Sending a key or a prompt to an address the owner did not choose
- Getting the installer to install something other than the released app
- A key, a prompt or an answer in the log

A program that already runs under your user account can read what you can read. The app does not protect against that.

## Check a release

`install.sh` carries the SHA-256 of its own DMG, and `SHA256SUMS` in the release lists every file. Both come from the same GitHub release, so they show a damaged download and a swapped DMG, not a takeover of the repository. The app is not notarized by Apple.
