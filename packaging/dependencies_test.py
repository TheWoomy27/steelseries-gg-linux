#!/usr/bin/env python3
"""Test setup dependencies without installing packages or downloading files."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

REPO = Path(__file__).resolve().parent.parent
HELPER = REPO / "packaging/dependencies.sh"


class DependenciesTest(unittest.TestCase):
    def run_shell(self, script, commands="", **variables):
        with tempfile.TemporaryDirectory(prefix="gg-deps-") as home:
            env = dict(os.environ, HOME=home, PREFIX=f"{home}/prefix", COMMANDS=commands)
            env.update(variables)
            harness = r'''
set -eu
repo=$REPO
. "$HELPER"
have() { case " $COMMANDS " in *" $1 "*) return 0;; *) return 1;; esac; }
note() { printf '%s\n' "$*"; }
step() { note "$*"; }
die() { note "$*"; exit 1; }
ask() { [ "${ACCEPT:-0}" = 1 ]; }
apt-cache() { printf '  Candidate: %s\n' "${APT_CANDIDATE:-1.0}"; }
sudo() {
    printf 'SUDO %s\n' "$*"
    case " $* " in *" ${FAIL_PACKAGE:-NO_FAILURE} "*) return 1;; esac
    [ "${SUDO_FAIL:-0}" = 0 ]
}
go() {
    case "$*" in
        version) [ "${GO_FAIL:-0}" = 0 ] || return 1
                 echo "go version go${GO_VERSION:-1.27.3} linux/amd64" ;;
        'env CGO_ENABLED') echo "${CGO_VALUE:-1}" ;;
    esac
}
cc() { cat >/dev/null; [ "${CC_FAIL:-0}" = 0 ]; }
'''
            result = subprocess.run(
                [os.environ.get("GG_TEST_SHELL", "sh"), "-c", harness + "\n" + script],
                env=dict(env, REPO=str(REPO), HELPER=str(HELPER)),
                text=True, capture_output=True,
            )
            return result

    def assert_success(self, result):
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_distribution_families(self):
        for distro, command, manager in [
            ("linuxmint ubuntu debian", "apt-get", "apt"),
            ("ubuntu debian", "apt-get", "apt"),
            ("debian", "apt-get", "apt"),
            ("fedora", "dnf", "dnf"),
            ("bazzite fedora", "rpm-ostree dnf", "rpm-ostree"),
            ("bazzite-nvidia fedora", "rpm-ostree dnf", "rpm-ostree"),
            ("bazzite fedora", "rpm-ostree", "rpm-ostree"),
            ("bazzite fedora", "dnf", "unknown"),
            ("cachyos arch", "pacman", "pacman"),
            ("opensuse", "zypper", "unknown"),
            ("fedora", "apt-get", "unknown"),
        ]:
            with self.subTest(distro=distro):
                r = self.run_shell(f'select_package_manager "{distro}"; echo "$manager"', command)
                self.assert_success(r)
                self.assertEqual(r.stdout.strip(), manager)

    def test_package_names(self):
        expected = {
            "apt": ["golang-go", "build-essential", "libreadline-dev", "p7zip-full", "pipewire-bin", "pulseaudio-utils", "ffmpeg"],
            "dnf": ["golang", "gcc", "readline-devel", "7zip", "pipewire-utils", "pulseaudio-utils", "ffmpeg-free"],
            "pacman": ["go", "gcc", "readline", "7zip", "pipewire", "libpulse", "ffmpeg"],
        }
        expected["rpm-ostree"] = expected["dnf"]
        for manager, names in expected.items():
            r = self.run_shell(f'manager={manager}; for key in go cc readline 7zip pipewire pactl ffmpeg; do package_for "$key"; done')
            self.assert_success(r)
            self.assertEqual(r.stdout.splitlines(), names)

    def test_apt_package_fallback(self):
        r = self.run_shell('manager=apt; package_for 7zip', APT_CANDIDATE="(none)")
        self.assert_success(r)
        self.assertEqual(r.stdout.strip(), "p7zip-full")
        r = self.run_shell('apt-cache() { case "$2" in 7zip) echo "Candidate: 1";; *) echo "Candidate: (none)";; esac; }; manager=apt; package_for 7zip')
        self.assert_success(r)
        self.assertEqual(r.stdout.strip(), "7zip")

    def test_extractor_names(self):
        for command in ("7z", "7zz", "7za"):
            self.assert_success(self.run_shell("sevenzip_found", command))
        self.assertNotEqual(self.run_shell("sevenzip_found").returncode, 0)

    def test_readline_link_failure_requires_development_package(self):
        r = self.run_shell('manager=dnf; check_required; echo "$missing"', "go cc curl 7zz", CC_FAIL="1")
        self.assert_success(r)
        self.assertEqual(r.stdout.strip(), "readline-devel")

    def test_no_missing_build_dependencies(self):
        r = self.run_shell('manager=apt; check_required; test -z "$missing"', "go cc curl 7zz")
        self.assert_success(r)

    def test_apt_refreshes_only_once(self):
        r = self.run_shell('manager=apt; apt_updated=0; install_packages gcc; install_packages curl', "sudo")
        self.assert_success(r)
        self.assertEqual(r.stdout.count("SUDO apt-get update"), 1)
        self.assertIn("SUDO apt-get install -y gcc", r.stdout)

    def test_failed_apt_refresh_does_not_install(self):
        r = self.run_shell('manager=apt; apt_updated=0; install_packages gcc', "sudo", SUDO_FAIL="1")
        self.assertNotEqual(r.returncode, 0)
        self.assertNotIn("install -y", r.stdout)

    def test_no_sudo_prints_help(self):
        r = self.run_shell('manager=dnf; install_packages gcc')
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("administrator", r.stdout)
        self.assertNotIn("SUDO", r.stdout)

    def test_electron_override_must_exist(self):
        r = self.run_shell('electron_dir="$HOME/electron"; electron_found', "electron39", GG_LINUX_ELECTRON="/missing/electron")
        self.assertNotEqual(r.returncode, 0)

    def test_local_electron_in_path_with_spaces(self):
        r = self.run_shell('electron_dir="$HOME/a path/electron"; mkdir -p "$electron_dir/node_modules/electron/dist"; touch "$electron_dir/node_modules/electron/dist/electron"; chmod +x "$electron_dir/node_modules/electron/dist/electron"; electron_found', GG_LINUX_ELECTRON="")
        self.assert_success(r)

    def setup_run(self, extra="", commands="", **variables):
        # Override only distribution detection. Exercise the real coordinator.
        return self.run_shell('select_package_manager() { manager=dnf; }; ' + extra + '\nsetup_dependencies', commands, **variables)

    def test_declined_required_install_prints_manual_command(self):
        r = self.setup_run()
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("sudo dnf install golang 7zip curl gcc readline-devel", r.stdout)
        self.assertNotIn("SUDO", r.stdout)

    def test_required_install_rechecks_commands(self):
        r = self.setup_run(commands="sudo", ACCEPT="1")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("SUDO dnf install -y", r.stdout)
        self.assertIn("Required dependencies are missing", r.stdout)

    def test_required_install_success(self):
        r = self.setup_run('install_packages() { COMMANDS="$COMMANDS go cc curl 7zz"; };',
                           commands="electron39", ACCEPT="1")
        self.assert_success(r)
        self.assertIn("Required dependencies are ready", r.stdout)

    def test_optional_failure_does_not_block_other_packages(self):
        r = self.setup_run(commands="go cc curl 7zz electron39 sudo", ACCEPT="1", FAIL_PACKAGE="wireplumber")
        self.assert_success(r)
        self.assertIn("Could not install wireplumber", r.stdout)
        self.assertIn("SUDO dnf install -y ffmpeg-free", r.stdout)
        self.assertIn("Required dependencies are ready", r.stdout)
        self.assertNotIn("SUDO dnf install -y gpu-screen-recorder", r.stdout)

    def test_old_go_has_actionable_error(self):
        r = self.setup_run(commands="go cc curl 7zz", GO_VERSION="1.18.1")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("Go 1.21 or newer", r.stdout)
        self.assertIn("https://go.dev/doc/install", r.stdout)

    def test_go_toolchain_failure(self):
        r = self.setup_run('go() { if [ "${GOTOOLCHAIN:-}" = local ]; then echo "go version go1.21.0 linux/amd64"; else return 1; fi; };', commands="go cc curl 7zz")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("Go cannot load the toolchain", r.stdout)

    def test_disabled_cgo(self):
        r = self.setup_run(commands="go cc curl 7zz", CGO_VALUE="0")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("CGO_ENABLED=1 ./setup.sh", r.stdout)

    def test_missing_electron_prints_local_install_command(self):
        r = self.setup_run(commands="go cc curl 7zz", GG_LINUX_ELECTRON="")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn('npm install --prefix "', r.stdout)
        self.assertIn('electron@39', r.stdout)
        self.assertIn('Electron is missing', r.stdout)

    def bazzite_run(self, commands="", **variables):
        return self.setup_run('select_package_manager() { manager=rpm-ostree; };',
                              commands=commands, **variables)

    def test_bazzite_install_stages_packages(self):
        r = self.run_shell('manager=rpm-ostree; reboot_needed=0; install_packages gcc readline-devel; echo "REBOOT=$reboot_needed"', "sudo")
        self.assert_success(r)
        self.assertIn("SUDO rpm-ostree install --idempotent --allow-inactive -y gcc readline-devel", r.stdout)
        self.assertIn("REBOOT=1", r.stdout)
        self.assertNotIn("--reboot", r.stdout)
        self.assertNotIn("--apply-live", r.stdout)

    def test_bazzite_install_failure_does_not_set_reboot(self):
        r = self.run_shell('manager=rpm-ostree; reboot_needed=0; if install_packages gcc; then exit 2; fi; echo "REBOOT=$reboot_needed"', "sudo", SUDO_FAIL="1")
        self.assert_success(r)
        self.assertIn("REBOOT=0", r.stdout)

    def test_bazzite_required_packages_stop_before_go(self):
        r = self.bazzite_run(commands="sudo", ACCEPT="1")
        self.assert_success(r)
        self.assertIn("SUDO rpm-ostree install --idempotent --allow-inactive -y golang 7zip curl gcc readline-devel npm", r.stdout)
        self.assertIn("Reboot required", r.stdout)
        self.assertIn("same options", r.stdout)
        self.assertNotIn("go version", r.stdout)
        self.assertNotIn("Required dependencies are ready", r.stdout)
        self.assertNotIn("SUDO dnf", r.stdout)

    def test_bazzite_existing_electron_does_not_require_npm(self):
        r = self.bazzite_run(commands="sudo electron39", ACCEPT="1")
        self.assert_success(r)
        self.assertIn("Reboot required", r.stdout)
        self.assertNotIn("readline-devel npm", r.stdout)

    def test_bazzite_declined_install_has_manual_command(self):
        r = self.bazzite_run()
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("sudo rpm-ostree install --idempotent --allow-inactive golang", r.stdout)
        self.assertNotIn("SUDO", r.stdout)
        self.assertNotIn("Reboot required", r.stdout)

    def test_bazzite_failed_required_install_stays_an_error(self):
        r = self.bazzite_run(commands="sudo", ACCEPT="1", SUDO_FAIL="1")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("Package installation failed", r.stdout)
        self.assertNotIn("Reboot required", r.stdout)

    def test_bazzite_optional_packages_also_require_reboot(self):
        r = self.bazzite_run(commands="go cc curl 7zz electron39 sudo", ACCEPT="1", FAIL_PACKAGE="wireplumber")
        self.assert_success(r)
        self.assertIn("Could not install wireplumber", r.stdout)
        self.assertIn("SUDO rpm-ostree install --idempotent --allow-inactive -y ffmpeg-free", r.stdout)
        self.assertIn("Reboot required", r.stdout)
        self.assertNotIn("Required dependencies are ready", r.stdout)

    def test_bazzite_all_optional_failures_do_not_require_reboot(self):
        r = self.bazzite_run(commands="go cc curl 7zz electron39 sudo", ACCEPT="1", SUDO_FAIL="1")
        self.assert_success(r)
        self.assertNotIn("Reboot required", r.stdout)
        self.assertIn("Required dependencies are ready", r.stdout)

    def test_bazzite_after_reboot_can_continue(self):
        r = self.bazzite_run(commands="go cc curl 7zz electron39 pw-dump wpctl pactl ffmpeg wl-copy xdg-open")
        self.assert_success(r)
        self.assertIn("Required dependencies are ready", r.stdout)
        self.assertNotIn("Reboot required", r.stdout)
        self.assertNotIn("SUDO", r.stdout)

    def test_bazzite_existing_npm_does_not_get_layered(self):
        r = self.bazzite_run(commands="sudo npm", ACCEPT="1")
        self.assert_success(r)
        self.assertIn("Reboot required", r.stdout)
        self.assertNotIn("readline-devel npm", r.stdout)

    def test_launcher_finds_local_electron(self):
        with tempfile.TemporaryDirectory(prefix="gg launcher ") as temp:
            prefix = Path(temp)
            launcher = prefix / "bin/steelseries-gg"
            launcher.parent.mkdir()
            launcher.write_bytes((REPO / "packaging/steelseries-gg").read_bytes())
            launcher.chmod(0o755)
            app = prefix / "data/client/app"
            app.mkdir(parents=True)
            (app / "package.json").write_text("{}")
            electron = prefix / "lib/steelseries-gg/electron/node_modules/electron/dist/electron"
            electron.parent.mkdir(parents=True)
            electron.write_text('#!/bin/sh\nprintf "LOCAL_ELECTRON %s\\n" "$1"\n')
            electron.chmod(0o755)
            # Avoid contact with the user's running service.
            pgrep = launcher.parent / "pgrep"
            pgrep.write_text("#!/bin/sh\nexit 0\n")
            pgrep.chmod(0o755)
            env = dict(os.environ, GG_LINUX_DATA=str(prefix / "data"),
                       GG_LINUX_ELECTRON="", GG_LINUX_GGD="", LD_PRELOAD="",
                       PATH=str(launcher.parent) + os.pathsep + os.environ["PATH"])
            r = subprocess.run([str(launcher)], env=env, text=True, capture_output=True)
            self.assert_success(r)
            self.assertIn(f"LOCAL_ELECTRON {app}", r.stdout)

    def test_help_and_missing_installer_argument(self):
        r = subprocess.run([str(REPO / "setup.sh"), "--help"], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0)
        self.assertIn("--deps-only", r.stdout)
        r = subprocess.run([str(REPO / "setup.sh"), "--installer"], capture_output=True, text=True)
        self.assertEqual(r.returncode, 2)
        self.assertIn("needs a path", r.stderr)


if __name__ == "__main__":
    unittest.main()
