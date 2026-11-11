#!/bin/sh
# One-step install of gg-linux for the current user:
#
#   1. checks for what it needs (and offers to install it on Arch-based
#      systems),
#   2. downloads SteelSeries GG's Windows installer from steelseries.com
#      (or takes one you already have),
#   3. builds gg-linux, imports the installer and installs everything in
#      ~/.local,
#   4. sets up device access (asks for sudo) and starts ggd at login.
#
# Run it again to update: it downloads a newer GG when there is one,
# rebuilds and restarts ggd.
#
# Usage: ./setup.sh [--installer PATH] [--yes] [--no-udev] [--no-service]
#   --installer PATH  use this SteelSeriesGG<version>Setup.exe, no download
#   --yes             answer yes to every question (packages, sudo)
#   --no-udev         leave device access alone
#   --no-service      do not enable or restart the ggd user service
set -eu

repo=$(cd "$(dirname "$0")" && pwd)
url="https://steelseries.com/gg/downloads/gg/latest/windows"
cache="${XDG_CACHE_HOME:-$HOME/.cache}/gg-linux"
data="${GG_LINUX_DATA:-${XDG_DATA_HOME:-$HOME/.local/share}/steelseries-gg}"
bindir="${PREFIX:-$HOME/.local}/bin"
installer=""
yes=0
udev=1
service=1

while [ $# -gt 0 ]; do
    case "$1" in
    --installer) installer="$2"; shift 2 ;;
    --installer=*) installer="${1#*=}"; shift ;;
    --yes|-y) yes=1; shift ;;
    --no-udev) udev=0; shift ;;
    --no-service) service=0; shift ;;
    -h|--help) sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "Unknown option: $1 (see --help)" >&2; exit 2 ;;
    esac
done

step() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }
die() { printf '\033[31mError:\033[0m %s\n' "$*" >&2; exit 1; }

# ask QUESTION: yes with --yes, else asks on the terminal (no terminal: no).
ask() {
    [ "$yes" = 1 ] && return 0
    (: </dev/tty) 2>/dev/null || return 1
    printf '%s [y/N] ' "$1" >/dev/tty
    read -r reply </dev/tty || return 1
    case "$reply" in [yY]*) return 0 ;; *) return 1 ;; esac
}

have() { command -v "$1" >/dev/null 2>&1; }

electron_found() {
    [ -n "${GG_LINUX_ELECTRON:-}" ] && return 0
    for e in electron37 electron39 electron38 electron36 electron35 electron34 electron33 electron32 electron31 electron; do
        have "$e" && return 0
    done
    return 1
}

[ "$(id -u)" = 0 ] && die "run this as your own user, not root (it asks for sudo when it needs it)."

# --- 1. What it needs -------------------------------------------------------
# Package names are Arch's; other distributions name some differently.
missing=""
optional=""
uses=""
check() {
    have go || missing="$missing go"
    have 7z || missing="$missing 7zip"
    have curl || missing="$missing curl"
    have cc || missing="$missing gcc"
    [ -f /usr/include/readline/readline.h ] || missing="$missing readline"
    # Electron 37 is what gg-linux was built on; Arch now ships 39, which works too.
    electron_found || missing="$missing electron39"
}
want() { # COMMAND PACKAGE USE
    have "$1" && return 0
    optional="$optional $2"
    uses="$uses$(printf '\n      %-20s %s' "$2" "$3")"
}

step "Checking what gg-linux needs"
check
want pw-dump pipewire "Sonar, Prism audio effects"
want wpctl wireplumber "Sonar device volumes"
want pactl libpulse "Moments audio"
want ffmpeg ffmpeg "Moments clips"
want gpu-screen-recorder gpu-screen-recorder "Moments recording"
want wl-copy wl-clipboard "text macros, sharing clips"
want xdg-open xdg-utils "opening links and files"

if [ -n "$missing$optional" ]; then
    [ -n "$missing" ] && note "Needed:$missing"
    [ -n "$optional" ] && note "Recommended:$uses"
    distro=$( (. /etc/os-release && echo "${ID:-} ${ID_LIKE:-}") 2>/dev/null || true)
    case " $distro " in
    *" arch "*)
        if ask "Install$missing$optional with pacman (sudo)?"; then
            # shellcheck disable=SC2086
            sudo pacman -S --needed $missing $optional
        fi
        ;;
    *)
        note "Install them with your package manager (readline as its development"
        note "package). For another Electron (37 or newer), set GG_LINUX_ELECTRON"
        note "to its path."
        ;;
    esac
    missing=""
    check
    [ -z "$missing" ] || die "still missing:$missing"
fi
note "All set."

# --- 2. SteelSeries GG's installer -------------------------------------------
if [ -z "$installer" ]; then
    step "Getting SteelSeries GG from steelseries.com"
    mkdir -p "$cache"
    # The link redirects to the current SteelSeriesGG<version>Setup.exe.
    final=$(curl -fsSIL -o /dev/null -w '%{url_effective}' "$url") || die "could not reach $url"
    name=$(basename "$final")
    case "$name" in *.exe) ;; *) die "unexpected download $final" ;; esac
    size=$(curl -fsSIL "$final" | tr -d '\r' | awk 'tolower($1)=="content-length:" {n=$2} END {print n}')
    installer="$cache/$name"
    if [ -f "$installer" ] && [ -n "$size" ] && [ "$(wc -c <"$installer")" = "$size" ]; then
        note "$name is already downloaded."
    else
        note "Downloading $name ($((${size:-0} / 1048576)) MB)..."
        curl -fL --progress-bar -o "$installer.part" "$final" || die "download failed"
        mv "$installer.part" "$installer"
        # Keep only the newest installer.
        find "$cache" -maxdepth 1 -name 'SteelSeriesGG*Setup.exe' ! -name "$name" -delete
    fi
fi
[ -f "$installer" ] || die "no installer at $installer"
[ "$(head -c 2 "$installer")" = "MZ" ] || die "$installer is not a Windows installer"
installer="$(cd "$(dirname "$installer")" && pwd)/$(basename "$installer")"

# --- 3. Build, import, install -----------------------------------------------
step "Building gg-linux"
cd "$repo"
go build -o bin/ ./cmd/...

name=$(basename "$installer")
imported=""
[ -f "$data/import.json" ] && imported=$(sed -n 's/.*"installer": *"\([^"]*\)".*/\1/p' "$data/import.json")
if [ "$imported" = "$name" ] && [ -f "$data/client/app/package.json" ]; then
    step "SteelSeries GG ($name) is already imported"
else
    step "Importing $name"
    note "This unpacks GG's client, device specifications and assets; it takes a minute."
    GG_LINUX_DATA="$data" bin/gg-setup "$installer"
fi

step "Installing for $(id -un)"
GG_LINUX_SETUP=1 "$repo/packaging/install.sh"

# --- 4. Device access and ggd ------------------------------------------------
rules=/etc/udev/rules.d/70-steelseries-gg.rules
if [ "$udev" = 1 ]; then
    if [ -f "$rules" ] && cmp -s "$rules" "$repo/packaging/70-steelseries-gg.rules"; then
        step "Device access is set up"
    else
        step "Device access"
        note "A udev rule lets your user talk to SteelSeries devices and play macros."
        if ask "Install $rules (sudo)?"; then
            sudo install -Dm644 "$repo/packaging/70-steelseries-gg.rules" "$rules"
            sudo udevadm control --reload
            sudo udevadm trigger
            note "Done. Replug your devices if GG does not see them."
        else
            note "Skipped. To do it later:"
            note "  sudo install -Dm644 $repo/packaging/70-steelseries-gg.rules $rules && sudo udevadm control --reload && sudo udevadm trigger"
        fi
    fi
fi

if [ "$service" = 1 ]; then
    step "Starting ggd"
    systemctl --user daemon-reload
    systemctl --user enable steelseries-ggd.service >/dev/null 2>&1 || true
    systemctl --user restart steelseries-ggd.service
    note "ggd now starts with your desktop session."
fi

if ! id -nG | tr ' ' '\n' | grep -qx input; then
    note ""
    note "Recording macros in GG reads your keyboard and mouse, which needs the"
    note "input group (it can read all keyboards): sudo usermod -aG input $(id -un), then log in again."
fi

step "Done"
note "Open SteelSeries GG from your application menu, or run: $bindir/steelseries-gg"
