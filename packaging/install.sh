#!/bin/sh
# Installs gg-linux for the current user from a source checkout: ggd,
# ggctl, gg-setup and the steelseries-gg launcher in $PREFIX/bin
# (~/.local/bin), a desktop entry, a systemd user service that starts ggd
# with the graphical session and, when an installer has been imported,
# GG's icon and an up-to-date Linux bootstrap in the imported client.
#
# Import your installer first (bin/gg-setup SteelSeriesGG<version>Setup.exe),
# then run this, and again after updating gg-linux. Start ggd at login with
#   systemctl --user enable --now steelseries-ggd.service
set -e

repo=$(cd "$(dirname "$0")/.." && pwd)
prefix=${PREFIX:-$HOME/.local}
bindir=$prefix/bin
datahome=${XDG_DATA_HOME:-$HOME/.local/share}
confighome=${XDG_CONFIG_HOME:-$HOME/.config}
data=${GG_LINUX_DATA:-$datahome/steelseries-gg}

cd "$repo"
go build -o bin/ ./cmd/...
install -Dm755 -t "$bindir" bin/ggd bin/ggctl bin/gg-setup
install -Dm755 packaging/steelseries-gg "$bindir/steelseries-gg"

mkdir -p "$datahome/applications" "$confighome/systemd/user"
sed "s|@BINDIR@|$bindir|g" packaging/steelseries-gg.desktop > "$datahome/applications/steelseries-gg.desktop"
sed "s|@BINDIR@|$bindir|g" packaging/steelseries-ggd.service > "$confighome/systemd/user/steelseries-ggd.service"
systemctl --user daemon-reload 2>/dev/null || true

if [ -f "$data/client/app/package.json" ]; then
    GG_LINUX_DATA=$data "$bindir/gg-setup" --bootstrap
    # GG's own icon, from your installer.
    for size in 16 24 32 48 256; do
        icon="$data/gg/tray/ok-$size.png"
        [ -f "$icon" ] && install -Dm644 "$icon" "$datahome/icons/hicolor/${size}x$size/apps/steelseries-gg.png"
    done
    command -v gtk-update-icon-cache >/dev/null && gtk-update-icon-cache -q -t "$datahome/icons/hicolor" 2>/dev/null || true
else
    echo "No imported installer in $data yet: run gg-setup, then this script again." >&2
fi

if [ ! -f /etc/udev/rules.d/70-steelseries-gg.rules ]; then
    echo "For device access, as root:" >&2
    echo "  cp $repo/packaging/70-steelseries-gg.rules /etc/udev/rules.d/ && udevadm control --reload && udevadm trigger" >&2
fi
echo "Installed to $bindir. Start ggd at login: systemctl --user enable --now steelseries-ggd.service"
