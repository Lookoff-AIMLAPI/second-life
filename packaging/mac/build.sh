#!/bin/bash
# Builds "Second Life.app" and a DMG. Needs only Go and the Command Line Tools.
#
#   VERSION    release version, default 0.0.0-dev
#   UNIVERSAL  1 for arm64 + x86_64, default: this machine's architecture
#   SLIFE_BIN, TRAY_BIN  use these prebuilt binaries instead of building
#   DIST       output directory, default <repo>/dist
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
app_name="Second Life"
min_macos="13.0"

VERSION="${VERSION:-0.0.0-dev}"
VERSION="${VERSION#v}"
UNIVERSAL="${UNIVERSAL:-0}"
DIST="${DIST:-$root/dist}"
SLIFE_BIN="${SLIFE_BIN:-}"
TRAY_BIN="${TRAY_BIN:-}"

if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "VERSION must look like 1.2.3 or 1.2.3-suffix, got: $VERSION" >&2
  exit 1
fi
if [[ -n "$SLIFE_BIN" && -z "$TRAY_BIN" ]] || [[ -z "$SLIFE_BIN" && -n "$TRAY_BIN" ]]; then
  echo "Set both SLIFE_BIN and TRAY_BIN, or neither" >&2
  exit 1
fi

work="$(mktemp -d "${TMPDIR:-/tmp}/slife-mac.XXXXXX")"
mount_point=""
cleanup() {
  if [[ -n "$mount_point" ]]; then
    hdiutil detach "$mount_point" -quiet || hdiutil detach "$mount_point" -force -quiet || true
  fi
  rm -rf "${work:?}"
}
trap cleanup EXIT

go_build() { # <arch> <cgo> <package> <output>
  echo "build $3 ($1)"
  (
    cd "$root"
    GOOS=darwin GOARCH="$1" CGO_ENABLED="$2" MACOSX_DEPLOYMENT_TARGET="$min_macos" \
      CGO_CFLAGS="-O2 -mmacosx-version-min=$min_macos" \
      CGO_LDFLAGS="-mmacosx-version-min=$min_macos" \
      go build -trimpath -ldflags "-s -w -X github.com/Lookoff-AIMLAPI/second-life/internal/bridge.Version=$VERSION" -o "$4" "$3"
  )
}

if [[ "$UNIVERSAL" == "1" ]]; then
  archs=(arm64 amd64)
  label="universal"
else
  archs=("$(go env GOHOSTARCH)")
  label="${archs[0]}"
fi

if [[ -z "$SLIFE_BIN" ]]; then
  slife_parts=()
  tray_parts=()
  for arch in "${archs[@]}"; do
    go_build "$arch" 0 ./cmd/slife "$work/slife-$arch"
    go_build "$arch" 1 ./cmd/slife-tray "$work/slife-tray-$arch"
    slife_parts+=("$work/slife-$arch")
    tray_parts+=("$work/slife-tray-$arch")
  done
  SLIFE_BIN="$work/slife"
  TRAY_BIN="$work/slife-tray"
  lipo -create "${slife_parts[@]}" -output "$SLIFE_BIN"
  lipo -create "${tray_parts[@]}" -output "$TRAY_BIN"
fi

for bin in "$SLIFE_BIN" "$TRAY_BIN"; do
  for arch in "${archs[@]}"; do
    lipo "$bin" -verify_arch "${arch/amd64/x86_64}" || { echo "$bin has no $arch code" >&2; exit 1; }
  done
done

# Icon: draw once at 1024 px, scale down, pack.
iconset="$work/AppIcon.iconset"
mkdir "$iconset"
(cd "$work" && sips -j "$here/icon.js" >/dev/null)
if [[ ! -s "$work/icon.png" ]]; then
  echo "sips did not draw the icon" >&2
  exit 1
fi
for size in 16 32 128 256 512; do
  sips -z "$size" "$size" "$work/icon.png" --out "$iconset/icon_${size}x${size}.png" >/dev/null
  sips -z "$((size * 2))" "$((size * 2))" "$work/icon.png" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done

# App bundle, assembled outside the output directory so a failure leaves the old one alone.
stage="$work/stage"
app="$stage/$app_name.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
install -m 0755 "$SLIFE_BIN" "$app/Contents/MacOS/slife"
install -m 0755 "$TRAY_BIN" "$app/Contents/MacOS/slife-tray"
iconutil -c icns "$iconset" -o "$app/Contents/Resources/AppIcon.icns"
cp "$root/LICENSE" "$root/THIRD_PARTY_NOTICES.md" "$app/Contents/Resources/"
# Bundle versions are numeric only.
sed "s/@VERSION@/${VERSION%%-*}/g" "$here/Info.plist.in" > "$app/Contents/Info.plist"
printf 'APPL????' > "$app/Contents/PkgInfo"
plutil -lint "$app/Contents/Info.plist"

# No developer certificate: an ad-hoc signature is what lets arm64 run it at all.
xattr -cr "$app"
codesign --force --deep -s - "$app"
codesign --verify --deep --strict --verbose=2 "$app"

mkdir -p "$DIST"
final_app="$DIST/$app_name.app"
rm -rf "${final_app:?}"
ditto "$app" "$final_app"
codesign --verify --deep --strict "$final_app"

ln -s /Applications "$stage/Applications"
dmg="$DIST/Second-Life-$VERSION-mac-$label.dmg"
# hdiutil now and then reports "Resource busy" on loaded CI runners; try again.
for attempt in 1 2 3; do
  if hdiutil create -quiet -ov -volname "$app_name" -srcfolder "$stage" -fs HFS+ -format UDZO "$dmg"; then
    break
  fi
  if [[ "$attempt" == "3" ]]; then
    echo "hdiutil create failed" >&2
    exit 1
  fi
  sleep 5
done

# Check what a user will get.
hdiutil verify -quiet "$dmg"
mkdir "$work/mnt"
hdiutil attach "$dmg" -quiet -readonly -nobrowse -noautoopen -mountpoint "$work/mnt"
mount_point="$work/mnt"
ls -la "$mount_point" "$mount_point/$app_name.app/Contents/MacOS"
[[ "$(readlink "$mount_point/Applications")" == "/Applications" ]]
codesign --verify --deep --strict --verbose=2 "$mount_point/$app_name.app"
hdiutil detach "$mount_point" -quiet
mount_point=""

# One file to send: the DMG and an installer that knows its checksum.
sum="$(shasum -a 256 "$dmg" | cut -d' ' -f1)"
kit="$work/Second-Life-$VERSION"
mkdir "$kit"
cp "$dmg" "$kit/"
sed -e "s/@SHA256@/$sum/" -e "s/@TAG@/v$VERSION/" -e "s/@DMG@/$(basename "$dmg")/" "$root/packaging/mac/install.sh" > "$kit/install.sh"
chmod 755 "$kit/install.sh"
grep -q "$sum" "$kit/install.sh"
cp "$kit/install.sh" "$DIST/install.sh"
zip="$DIST/Second-Life-$VERSION-mac-$label.zip"
rm -f "$zip"
(cd "$work" && /usr/bin/zip -qry "$zip" "$(basename "$kit")")

echo "zip: $zip"
echo "app: $final_app"
echo "dmg: $dmg"
shasum -a 256 "$dmg"
