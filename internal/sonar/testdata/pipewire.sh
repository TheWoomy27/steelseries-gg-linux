#!/bin/sh
# Starts a private PipeWire with WirePlumber for Sonar's PipeWire tests,
# isolated from the desktop's (no hardware, no D-Bus):
#
#   internal/sonar/testdata/pipewire.sh DIR        # start (or restart)
#   GG_TEST_PIPEWIRE=DIR GG_LINUX_DATA=~/.local/share/steelseries-gg \
#       go test ./internal/sonar -run OnPipeWire
#   internal/sonar/testdata/pipewire.sh DIR stop
#
# DIR becomes the instance's XDG_RUNTIME_DIR; its logs go there too.
set -e
[ -n "$1" ] || { echo "usage: $0 DIR [stop]" >&2; exit 2; }
here=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$1"
dir=$(cd "$1" && pwd)
chmod 700 "$dir"

for p in pipewire wireplumber; do
    [ -f "$dir/$p.pid" ] && kill "$(cat "$dir/$p.pid")" 2>/dev/null || true
    rm -f "$dir/$p.pid"
done
[ "${2:-}" = stop ] && exit 0
sleep 0.5

env -i HOME="$HOME" PATH="$PATH" XDG_RUNTIME_DIR="$dir" \
    setsid pipewire -c "$here/pipewire.conf" >"$dir/pipewire.log" 2>&1 &
echo $! >"$dir/pipewire.pid"
sleep 1.5
# Linking policy only: no hardware monitors.
mkdir -p "$dir/wireplumber-state" "$dir/wireplumber-config"
env -i HOME="$HOME" PATH="$PATH" XDG_RUNTIME_DIR="$dir" \
    XDG_STATE_HOME="$dir/wireplumber-state" XDG_CONFIG_HOME="$dir/wireplumber-config" \
    setsid wireplumber --profile policy >"$dir/wireplumber.log" 2>&1 &
echo $! >"$dir/wireplumber.pid"
sleep 1.5
