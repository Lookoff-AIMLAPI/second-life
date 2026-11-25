#!/bin/sh
# Installs Second Life on a Mac.
#   curl -fsSL https://github.com/Lookoff-AIMLAPI/second-life/releases/latest/download/install.sh | sh
# or, with the DMG next to this file:
#   sh install.sh [path/to/Second-Life-X.Y.Z-mac-universal.dmg]
# It asks before it changes anything. Codex, Claude and your keys stay as they
# are unless you say yes.
set -eu

APP_NAME="Second Life"
BUNDLE_ID="io.github.lookoff-aimlapi.second-life"
DEST="${SLIFE_DEST:-/Applications}" # tests only
TTY="${SLIFE_TTY:-/dev/tty}"        # tests only
EXPECTED_SHA256="${SLIFE_SHA256:-@SHA256@}" # these three are filled in by the build
TAG="@TAG@"
DMG_NAME="@DMG@"
REPO="https://github.com/Lookoff-AIMLAPI/second-life"
FORMER_NAME="Codex to Claude"
FORMER_ID="com.aimlapi.codex-to-claude"

say() { printf '%s\n' "$*"; }
die() { printf 'Stopped: %s\n' "$*" >&2; exit 1; }
ask() { # ask "question": yes only for y or yes
  printf '%s [y/N] ' "$1" >&2
  answer=""
  # sh may be reading this script from a pipe, so the answer comes from the terminal
  IFS= read -r answer <"$TTY" || answer=""
  case "$answer" in y | Y | yes | YES | Yes) return 0 ;; *) return 1 ;; esac
}

# A function, so that a download cut short runs nothing.
main() {

[ "$(uname -s)" = Darwin ] || die "this installer is for macOS"
[ "$(id -u)" -ne 0 ] || die "run it as yourself, not with sudo"
major="$(sw_vers -productVersion | cut -d. -f1)"
[ "$major" -ge 13 ] || die "macOS 13 or newer is needed"

dmg="${1:-}"
if [ -z "$dmg" ] && [ -f "$0" ]; then # run from a file: the DMG may lie next to it
  here="$(cd "$(dirname "$0")" && pwd -P)"
  case "$DMG_NAME" in
    @*) set -- "$here"/Second-Life-*-mac-*.dmg ;;
    *) set -- "$here/$DMG_NAME" ;; # only the one this script was made for
  esac
  for f in "$@"; do
    if [ -f "$f" ]; then dmg="$f"; fi
  done
fi
work="$(mktemp -d "${TMPDIR:-/tmp}/slife-install.XXXXXX")"
mounted=""
cleanup() {
  [ -z "$mounted" ] || hdiutil detach -quiet "$work/mnt" 2>/dev/null || hdiutil detach -quiet -force "$work/mnt" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

if [ -z "$dmg" ]; then
  case "$TAG$EXPECTED_SHA256" in
    *@*) die "put the DMG next to this script, or give its path" ;;
  esac
  # The script names its own release, so a newer one cannot slip in between.
  dmg="$work/$DMG_NAME"
  say "Downloading $DMG_NAME from $REPO, release $TAG."
  curl -fL --proto '=https' --proto-redir '=https' --progress-bar -o "$dmg" "$REPO/releases/download/$TAG/$DMG_NAME" || die "the download failed"
fi
[ -f "$dmg" ] || die "there is no file $dmg"

sum="$(shasum -a 256 "$dmg" | cut -d' ' -f1)"
case "$EXPECTED_SHA256" in
  @*) say "SHA-256: $sum (nothing to compare it with; check it with the sender)" ;;
  "$sum") say "SHA-256 matches." ;;
  *) die "the file is damaged or not the one this script was made for" ;;
esac

mkdir "$work/mnt"
hdiutil attach -quiet -nobrowse -readonly -mountpoint "$work/mnt" "$dmg" || die "the DMG does not open"
mounted=1
src="$work/mnt/$APP_NAME.app"
[ -d "$src" ] && [ ! -L "$src" ] || die "the DMG does not hold the app"
codesign --verify --deep --strict "$src" 2>/dev/null || die "the app inside is damaged"
id="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$src/Contents/Info.plist")"
[ "$id" = "$BUNDLE_ID" ] || die "the DMG holds another app"
new="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$src/Contents/Info.plist")"
app="$DEST/$APP_NAME.app"
[ ! -L "$app" ] || die "$app is a link"
old=""
if [ -d "$app" ]; then
  id="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$app/Contents/Info.plist" 2>/dev/null || true)"
  [ "$id" = "$BUNDLE_ID" ] || die "$app is another app"
  old="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$app/Contents/Info.plist" 2>/dev/null || true)"
fi
[ -w "$DEST" ] || die "you cannot write to $DEST"

say ""
if [ -n "$old" ]; then say "Update $APP_NAME $old to $new in $DEST."; else say "Install $APP_NAME $new to $DEST."; fi
ask "Go on?" || die "nothing was changed"

close_app() { # close_app <path of the app>: it closes cleanly on TERM
  tray="$1/Contents/MacOS/$2"
  pgrep -f "^$tray" >/dev/null 2>&1 || return 0
  say "Closing the running app."
  pkill -TERM -f "^$tray" || true
  n=0
  while pgrep -f "^$tray" >/dev/null 2>&1; do
    n=$((n + 1))
    [ "$n" -lt 15 ] || die "the app does not close; quit it from its menu and run this again"
    sleep 1
  done
}
close_app "$app" slife-tray
former="$DEST/$FORMER_NAME.app"
has_former=""
if [ -d "$former" ] && [ ! -L "$former" ] &&
  [ "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$former/Contents/Info.plist" 2>/dev/null || true)" = "$FORMER_ID" ]; then
  has_former=1
  close_app "$former" c2c-tray # it holds the same port
fi

# Copy next to the target first, then swap: a failed copy leaves the old app in place.
stage="$(mktemp -d "$DEST/.slife-new.XXXXXX")"
if ! ditto "$src" "$stage/$APP_NAME.app" || ! codesign --verify --deep --strict "$stage/$APP_NAME.app" 2>/dev/null; then
  rm -rf "$stage"
  die "the copy failed; the old app is untouched"
fi
if [ -n "$old" ]; then mv "$app" "$stage/old.app"; fi
if ! mv "$stage/$APP_NAME.app" "$app"; then
  [ -z "$old" ] || mv "$stage/old.app" "$app"
  rm -rf "$stage"
  die "could not put the app in place; the old one is back"
fi
rm -rf "$stage"
say "Installed $new."

if [ -n "$has_former" ]; then
  say ""
  say "Second Life used to be called $FORMER_NAME. Your key, models and Claude setup carry over."
  if ask "Move the old app to the Trash?"; then
    trash="$HOME/.Trash/$FORMER_NAME $(date +%Y%m%d-%H%M%S).app"
    if mv "$former" "$trash"; then say "It is in the Trash."; else say "It could not be moved; delete it yourself."; fi
    agent="$HOME/Library/LaunchAgents/$FORMER_ID.plist"
    if [ -f "$agent" ] && [ ! -L "$agent" ]; then
      rm -f "$agent"
      say "Its start at login is off; turn it on in Second Life if you want it."
    fi
  else
    say "Do not run both at once: they use the same port."
  fi
  say "Claude Desktop in gateway mode still points at the old app: run Set up Claude Desktop once."
fi

if xattr -p com.apple.quarantine "$app" >/dev/null 2>&1; then
  say ""
  say "macOS marks this copy as downloaded, and the app is not notarized by Apple, so macOS will not start it."
  say "Only if you trust where you got it, this script can clear the mark for this one app."
  if ask "Clear it?"; then
    xattr -dr com.apple.quarantine "$app"
  else
    die "installed, but macOS will not start it until the mark is cleared"
  fi
fi

slife="$app/Contents/MacOS/slife"
say ""
"$slife" doctor || true
say ""
say "Codex models need the Codex CLI and a ChatGPT sign-in. AIMLAPI models need only an AIMLAPI key."
if ask "Set up Codex now (it checks first, and asks before it installs)?"; then
  "$slife" codex <"$TTY" || say "Codex is not set up; you can do it later from the app's menu."
fi

[ -n "${SLIFE_NO_OPEN:-}" ] || open "$app"
say ""
say "Done. Look for the icon in the menu bar: Choose models for AIMLAPI, then Open Claude or Set up Claude Desktop."
}

main "$@"
