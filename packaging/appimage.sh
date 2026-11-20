#!/bin/sh
# Builds the gg-linux AppImage: ggd, ggctl, gg-setup and the launcher, with
# Electron, 7-Zip and the libraries ggd needs beyond glibc, and the
# first-run window that imports GG's installer. Nothing from SteelSeries
# goes in it. The release workflow (.github/workflows/release.yml) runs
# this on the oldest Ubuntu GitHub has, so the result runs on newer
# systems (glibc 2.34 or later).
#
# Usage: ELECTRON_DIR=DIR SEVENZIP=FILE [APPIMAGETOOL=FILE] packaging/appimage.sh [OUTDIR]
#   ELECTRON_DIR  an Electron release's files (the npm package's dist/)
#   SEVENZIP      7-Zip's 7zz or 7zzs (its License.txt beside it is shipped too)
#   APPIMAGETOOL  appimagetool; without it OUTDIR/AppDir is left to run
#                 as is (OUTDIR/AppDir/AppRun)
#   RUNTIME       the AppImage runtime appimagetool puts in front
#   UPDATE_INFO   update information, for AppImageUpdate (adds a .zsync)
#   VERSION       defaults to git describe
# OUTDIR defaults to dist/.
set -eu

repo=$(cd "$(dirname "$0")/.." && pwd)
out=${1:-$repo/dist}
: "${ELECTRON_DIR:?set ELECTRON_DIR to an Electron release (its dist folder)}"
: "${SEVENZIP:?set SEVENZIP to 7-Zip's 7zz}"
[ -x "$ELECTRON_DIR/electron" ] || { echo "no electron in $ELECTRON_DIR" >&2; exit 1; }
version=${VERSION:-$(git -C "$repo" describe --tags --always --dirty 2>/dev/null || echo dev)}
version=${version#v}

mkdir -p "$out"
out=$(cd "$out" && pwd)
appdir=$out/AppDir
rm -rf "$appdir"
gg=$appdir/usr/lib/steelseries-gg
share=$appdir/usr/share/steelseries-gg
mkdir -p "$appdir/usr/bin" "$gg/bin" "$share" "$appdir/usr/share/doc/gg-linux"

cd "$repo"
# ggd and ggctl link readline (golisp's REPL). The AppImage carries it
# beside them, found through their RPATH, which readline's own lookup of
# ncurses goes through too; no LD_LIBRARY_PATH reaches the programs ggd
# starts.
CGO_ENABLED=1 go build -trimpath -ldflags '-linkmode=external -extldflags=-Wl,--disable-new-dtags,-rpath,$ORIGIN/../lib' \
    -o "$appdir/usr/bin/" ./cmd/ggd ./cmd/ggctl ./cmd/gg-setup
ldd "$appdir/usr/bin/ggd" | awk '$2 == "=>" { print $3 }' | while read -r so; do
    case $(basename "$so") in
    libreadline.so*|libtinfo.so*|libncursesw.so*|libncurses.so*) cp -L "$so" "$appdir/usr/lib/" ;;
    esac
done
for doc in /usr/share/doc/libreadline8/copyright /usr/share/doc/libtinfo6/copyright; do
    [ -f "$doc" ] && cp "$doc" "$appdir/usr/share/doc/gg-linux/$(basename "$(dirname "$doc")").copyright"
done
cc -shared -fPIC -O2 -pthread -o "$gg/libgg-dragfix.so" packaging/dragfix.c -ldl
install -m755 packaging/steelseries-gg "$appdir/usr/bin/steelseries-gg"

# Electron, without its setuid sandbox helper (an AppImage can't keep one
# setuid): Chromium sandboxes with user namespaces instead.
cp -a "$ELECTRON_DIR" "$gg/electron"
rm -f "$gg/electron/chrome-sandbox"
install -m755 "$SEVENZIP" "$gg/bin/7zz"
if [ -f "$(dirname "$SEVENZIP")/License.txt" ]; then
    cp "$(dirname "$SEVENZIP")/License.txt" "$appdir/usr/share/doc/gg-linux/7-Zip.License.txt"
fi

cp -r packaging/appimage/firstrun "$share/firstrun"
cp packaging/70-steelseries-gg.rules "$share/"
printf '%s\n' "$version" >"$share/version"
cp LICENSE "$appdir/usr/share/doc/gg-linux/"

install -m755 packaging/appimage/AppRun "$appdir/AppRun"
cp packaging/appimage/steelseries-gg.desktop packaging/appimage/steelseries-gg.svg "$appdir/"
ln -s steelseries-gg.svg "$appdir/.DirIcon"

if [ -z "${APPIMAGETOOL:-}" ]; then
    echo "Built $appdir (no APPIMAGETOOL given: run it as $appdir/AppRun)"
    exit 0
fi
file=$out/SteelSeries_GG-$version-x86_64.AppImage
set --
[ -n "${RUNTIME:-}" ] && set -- "$@" --runtime-file "$RUNTIME"
[ -n "${UPDATE_INFO:-}" ] && set -- "$@" --updateinformation "$UPDATE_INFO"
rm -f "$file" "$file.zsync"
# appimagetool writes the .zsync into the folder it runs in.
cd "$out"
ARCH=x86_64 VERSION=$version "$APPIMAGETOOL" "$@" "$appdir" "$file"
if [ -n "${UPDATE_INFO:-}" ] && [ ! -f "$file.zsync" ]; then
    echo "appimagetool made no $file.zsync" >&2
    exit 1
fi
echo "Built $file"
