# Dependency checks for setup.sh. Package installs require ask() approval.

select_package_manager() {
    manager=unknown
    # Bazzite can include DNF, but host packages need rpm-ostree.
    case " $1 " in
        *" bazzite "*|*" bazzite-"*)
            have rpm-ostree && manager=rpm-ostree
            return 0 ;;
    esac
    case " $1 " in
        *" debian "*|*" ubuntu "*|*" linuxmint "*) have apt-get && manager=apt ;;
        *" fedora "*) have dnf && manager=dnf ;;
        *" arch "*) have pacman && manager=pacman ;;
    esac
    return 0
}

# APT changed several library names in Ubuntu 24.04 and Debian 13.
apt_package() {
    for candidate do
        if apt-cache policy "$candidate" 2>/dev/null | grep -Eq 'Candidate: [^ (]'; then
            printf '%s\n' "$candidate"
            return 0
        fi
    done
    printf '%s\n' "$1"
}

package_for() {
    case "$manager:$1" in
        apt:go) echo golang-go ;;
        apt:cc) echo build-essential ;;
        apt:readline) echo libreadline-dev ;;
        apt:7zip) apt_package p7zip-full 7zip ;;
        apt:pipewire) echo pipewire-bin ;;
        apt:pactl) echo pulseaudio-utils ;;
        dnf:go|rpm-ostree:go) echo golang ;;
        dnf:cc|rpm-ostree:cc) echo gcc ;;
        dnf:readline|rpm-ostree:readline) echo readline-devel ;;
        dnf:7zip|rpm-ostree:7zip) echo 7zip ;;
        dnf:pipewire|rpm-ostree:pipewire) echo pipewire-utils ;;
        dnf:pactl|rpm-ostree:pactl) echo pulseaudio-utils ;;
        dnf:ffmpeg|rpm-ostree:ffmpeg) echo ffmpeg-free ;;
        pacman:cc) echo gcc ;;
        pacman:pactl) echo libpulse ;;
        *) echo "$1" ;;
    esac
}

install_packages() {
    have sudo || { note "sudo is missing. Ask an administrator to run the commands below."; return 1; }
    case "$manager" in
        apt)
            if [ "$apt_updated" = 0 ]; then
                sudo apt-get update || return 1
                apt_updated=1
            fi
            sudo apt-get install -y "$@" ;;
        rpm-ostree)
            sudo rpm-ostree install --idempotent --allow-inactive -y "$@" || return 1
            reboot_needed=1 ;;
        dnf) sudo dnf install -y "$@" ;;
        pacman) sudo pacman -S --needed --noconfirm "$@" ;;
        *) return 1 ;;
    esac
}

manual_packages() {
    case "$manager" in
        apt) note "  sudo apt-get update && sudo apt-get install $*" ;;
        rpm-ostree) note "  sudo rpm-ostree install --idempotent --allow-inactive $*" ;;
        dnf) note "  sudo dnf install $*" ;;
        pacman) note "  sudo pacman -S --needed $*" ;;
        *) note "Install these dependencies with your package manager: $*" ;;
    esac
}

sevenzip_found() { have 7z || have 7zz || have 7za; }

electron_found() {
    if [ -n "${GG_LINUX_ELECTRON:-}" ]; then
        have "$GG_LINUX_ELECTRON"
        return
    fi
    [ -x "$electron_dir/node_modules/electron/dist/electron" ] && return 0
    for e in electron37 electron39 electron38 electron36 electron35 electron34 electron33 electron32 electron31 electron; do
        have "$e" && return 0
    done
    return 1
}

readline_found() {
    have cc || return 1
    probe=$(mktemp -d) || return 1
    if printf '#include <stdio.h>\n#include <readline/readline.h>\nint main(void) { return rl_initialize(); }\n' |
        cc -x c - -o "$probe/readline" -lreadline >/dev/null 2>&1; then
        rm -rf "$probe"
        return 0
    fi
    rm -rf "$probe"
    return 1
}

check_required() {
    missing=""
    have go || missing="$missing $(package_for go)"
    sevenzip_found || missing="$missing $(package_for 7zip)"
    have curl || missing="$missing curl"
    have cc || missing="$missing $(package_for cc)"
    readline_found || missing="$missing $(package_for readline)"
    # Include npm in the same deployment to avoid a second required reboot.
    if [ "$manager" = rpm-ostree ] && ! electron_found && ! have npm; then
        missing="$missing npm"
    fi
}

electron_help() {
    note "Install Electron 39 for your user, then run ./setup.sh again:"
    if ! have npm; then
        manual_packages npm
        if [ "$manager" = rpm-ostree ]; then
            note "After installing npm, reboot before you run the npm command below."
        fi
    fi
    note "  npm install --prefix \"$electron_dir\" electron@39"
    note "The launcher finds this runtime automatically. Do not run npm with sudo."
    note "For another runtime, set GG_LINUX_ELECTRON to its executable path."
    note "See docs/install.md for Electron libraries and distribution-specific steps."
}

reboot_for_dependencies() {
    step "Reboot required"
    note "rpm-ostree staged the packages for the next boot."
    note "Reboot when ready, then run ./setup.sh again with the same options."
    note "Setup stopped before the GG download, build, or service changes."
    exit 0
}

setup_dependencies() {
    step "Checking what gg-linux needs"
    distro=$( (. /etc/os-release && echo "${ID:-} ${ID_LIKE:-}") 2>/dev/null || true)
    select_package_manager "$distro"
    apt_updated=0
    reboot_needed=0
    electron_dir="${PREFIX:-$HOME/.local}/lib/steelseries-gg/electron"
    if [ "$manager" = rpm-ostree ]; then
        note "Bazzite stages system packages with rpm-ostree. Changes need a reboot."
        note "Setup will stop after staging packages. It will not reboot your system."
    fi
    check_required
    if [ -n "$missing" ]; then
        note "Required packages:$missing"
        manual_packages $missing
        if [ "$manager" != unknown ] && ask "Install the required packages with $manager (sudo)?"; then
            # Package names come from the mappings above.
            # shellcheck disable=SC2086
            if ! install_packages $missing; then
                note "Package installation failed. Check the package manager errors above."
                manual_packages $missing
            elif [ "$manager" = rpm-ostree ]; then
                reboot_for_dependencies
            fi
        fi
        check_required
        if [ -n "$missing" ]; then
            manual_packages $missing
            die "Required dependencies are missing:$missing. See docs/install.md, then run ./setup.sh again."
        fi
    fi

    # Go 1.21 introduced automatic toolchain downloads for go.mod.
    go_version=$(cd / && GOTOOLCHAIN=local go version 2>/dev/null) || die "Go does not run. See https://go.dev/doc/install."
    if ! printf '%s\n' "$go_version" | awk '{split($3,v,"."); sub(/^go/,"",v[1]); exit !(v[1]>1 || (v[1]==1 && v[2]>=21))}'; then
        die "Go 1.21 or newer is required to select the toolchain in go.mod. Install Go from https://go.dev/doc/install, then run ./setup.sh again."
    fi
    # go version can succeed with GOTOOLCHAIN=local even if go.mod needs
    # a newer version. Load the module before the installer download.
    if ! (cd "$repo" && go list -m >/dev/null && go version); then
        die "Go cannot load the toolchain in go.mod. Try: GOTOOLCHAIN=auto ./setup.sh. Check network access. See docs/install.md."
    fi
    [ "$(cd "$repo" && go env CGO_ENABLED)" = 1 ] || die "CGO is disabled. Run: CGO_ENABLED=1 ./setup.sh"

    # Keep Arch's packaged Electron option before the portable manual fallback.
    if ! electron_found && [ "$manager" = pacman ] && [ -z "${GG_LINUX_ELECTRON:-}" ]; then
        manual_packages electron39
        if ask "Install Electron 39 with pacman (sudo)?"; then
            if ! install_packages electron39; then
                note "Could not install electron39. Use the local installation below."
            fi
        fi
    fi
    if ! electron_found; then
        electron_help
        if [ -n "${GG_LINUX_ELECTRON:-}" ]; then
            die "GG_LINUX_ELECTRON is not executable: $GG_LINUX_ELECTRON"
        fi
        die "Electron is missing. Run the commands above, then run ./setup.sh again."
    fi

    optional=""
    uses=""
    want() {
        have "$1" && return 0
        pkg=$(package_for "$2")
        optional="$optional $pkg"
        uses="$uses$(printf '\n      %-20s %s' "$pkg" "$3")"
    }
    want pw-dump pipewire "Sonar, Prism audio effects"
    want wpctl wireplumber "Sonar device volumes"
    want pactl pactl "Moments audio"
    want ffmpeg ffmpeg "Moments clips"
    want wl-copy wl-clipboard "text macros, sharing clips"
    want xdg-open xdg-utils "opening links and files"
    if [ -n "$optional" ]; then
        note "Recommended packages:$uses"
        manual_packages $optional
        if [ "$manager" != unknown ] && ask "Install the recommended packages with $manager (sudo)?"; then
            # Install separately so an unavailable optional package does not block others.
            for pkg in $optional; do
                if ! install_packages "$pkg"; then
                    note "Could not install $pkg. Setup will continue. To retry:"
                    manual_packages "$pkg"
                fi
            done
        fi
    fi
    [ "$reboot_needed" = 0 ] || reboot_for_dependencies
    if ! have gpu-screen-recorder; then
        note "For GPU capture, install gpu-screen-recorder: https://git.dec05eba.com/gpu-screen-recorder/about/"
        note "See docs/install.md for capture alternatives and optional audio plugins."
    fi
    note "Required dependencies are ready."
}
