# Device access for gg-linux: packaging/70-steelseries-gg.rules installed
# in the system's udev rules. Sourced by setup.sh and install.sh with
# $repo set; setup_udev uses their step, note and ask.

udev_rules=/etc/udev/rules.d/70-steelseries-gg.rules
# distrobox mounts the host's root here. Inside a container, /etc and the
# udev daemon are the host's business: a rule in the container's /etc is
# read by nothing, and a rootless container's sudo cannot reload udev.
host_root=/run/host

in_container() { [ -n "${CONTAINER_ID:-}" ] && [ -d "$host_root/etc" ]; }

# udev_installed reports whether the system has the current rule.
udev_installed() {
    if in_container; then
        installed=$host_root$udev_rules
    else
        installed=$udev_rules
    fi
    [ -f "$installed" ] && cmp -s "$installed" "$repo/packaging/70-steelseries-gg.rules"
}

# shell_quote quotes $1 for a command line to copy (the repo's path can
# have spaces).
shell_quote() { printf "'%s'" "$(printf %s "$1" | sed "s/'/'\\\\''/g")"; }

udev_command() {
    echo "sudo install -Dm644 $(shell_quote "$repo/packaging/70-steelseries-gg.rules") $udev_rules && sudo udevadm control --reload && sudo udevadm trigger"
}

setup_udev() {
    if udev_installed; then
        step "Device access is set up"
        return 0
    fi
    step "Device access"
    note "A udev rule lets your user talk to SteelSeries devices, play macros and turn mouse motion."
    if in_container; then
        note "This is a container: the rule belongs to the host. In a terminal on the host, outside the container, run:"
        note "  $(udev_command)"
        note "then replug your devices if GG does not see them."
        return 0
    fi
    if ask "Install $udev_rules (sudo)?"; then
        sudo install -Dm644 "$repo/packaging/70-steelseries-gg.rules" "$udev_rules"
        sudo udevadm control --reload
        sudo udevadm trigger
        note "Done. Replug your devices if GG does not see them."
    else
        note "Skipped. To do it later:"
        note "  $(udev_command)"
    fi
}
