#!/usr/bin/env python3
"""Test the device access step (packaging/udev.sh) without root."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

REPO = Path(__file__).resolve().parent.parent
RULES = REPO / "packaging/70-steelseries-gg.rules"

HARNESS = r'''
set -eu
. "$REPO/packaging/udev.sh"
udev_rules=$ROOT/etc/udev/rules.d/70-steelseries-gg.rules
host_root=$ROOT/host
note() { printf '%s\n' "$*"; }
step() { note "$*"; }
ask() { [ "${ACCEPT:-0}" = 1 ]; }
sudo() { printf 'SUDO %s\n' "$*"; [ "${SUDO_FAIL:-0}" = 0 ]; }
setup_udev
echo END
'''


class UdevTest(unittest.TestCase):
    def run_setup(self, host_rule=False, container=False, **variables):
        with tempfile.TemporaryDirectory(prefix="gg-udev-") as root:
            if container:
                os.makedirs(f"{root}/host/etc")
            if host_rule:
                # The harness's udev_rules is $ROOT/etc/...; a container
                # finds the host's under host_root.
                rule = Path(f"{root}/etc/udev/rules.d/70-steelseries-gg.rules")
                if container:
                    rule = Path(f"{root}/host{rule}")
                rule.parent.mkdir(parents=True)
                rule.write_bytes(RULES.read_bytes())
            env = {k: v for k, v in os.environ.items() if k != "CONTAINER_ID"}
            env.update(REPO=str(REPO), ROOT=root, repo=str(REPO), **variables)
            if container:
                env["CONTAINER_ID"] = "apps"
            return subprocess.run([os.environ.get("GG_TEST_SHELL", "sh"), "-c", HARNESS],
                                  env=env, text=True, capture_output=True)

    def test_host_installs_with_sudo(self):
        r = self.run_setup(ACCEPT="1")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("SUDO install -Dm644", r.stdout)
        self.assertIn("SUDO udevadm control --reload", r.stdout)

    def test_host_rule_present(self):
        r = self.run_setup(host_rule=True)
        self.assertIn("Device access is set up", r.stdout)
        self.assertNotIn("SUDO", r.stdout)

    def test_container_leaves_the_host_to_the_user(self):
        # In a distrobox, /etc is the container's and udev the host's: no
        # rule goes into the container, sudo is not run (udevadm would
        # fail and stop setup.sh), and the user gets the host command.
        r = self.run_setup(container=True, ACCEPT="1", SUDO_FAIL="1")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("END", r.stdout)
        self.assertNotIn("SUDO", r.stdout)
        self.assertIn("on the host", r.stdout)
        self.assertIn("udevadm trigger", r.stdout)

    def test_container_sees_the_host_rule(self):
        r = self.run_setup(container=True, host_rule=True, ACCEPT="1")
        self.assertIn("Device access is set up", r.stdout)
        self.assertNotIn("SUDO", r.stdout)


if __name__ == "__main__":
    unittest.main()
