#!/usr/bin/env python3
"""Test the AppImage's AppRun: the service and menu entry it writes (for
an AppImage path with spaces and quotes), following the AppImage when it
moves, and leaving install.sh's files alone. The programs it runs are
stubbed out."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

REPO = Path(__file__).resolve().parent.parent
SHELL = os.environ.get("GG_TEST_SHELL", "sh")


class AppRunTest(unittest.TestCase):
    def setUp(self):
        tmp = Path(tempfile.mkdtemp(prefix="gg-appimage-"))
        self.addCleanup(shutil.rmtree, tmp)
        self.tmp = tmp
        appdir = tmp / "AppDir"
        (appdir / "usr/bin").mkdir(parents=True)
        shutil.copy(REPO / "packaging/appimage/AppRun", appdir / "AppRun")
        shutil.copy(REPO / "packaging/appimage/steelseries-gg.svg", appdir)
        stubs = {
            # The launcher prints what AppRun hands it.
            "steelseries-gg": 'echo "launcher GG_LINUX_GGD=$GG_LINUX_GGD GG_LINUX_ELECTRON=$GG_LINUX_ELECTRON args=$*"',
            "gg-setup": 'echo "gg-setup $*"',
            "ggd": 'echo "ggd GG_LINUX_CLIENT=$GG_LINUX_CLIENT args=$*"',
        }
        for name, body in stubs.items():
            (appdir / "usr/bin" / name).write_text("#!/bin/sh\n" + body + "\n")
            (appdir / "usr/bin" / name).chmod(0o755)
        bin_stubs = tmp / "stubs"
        bin_stubs.mkdir()
        (bin_stubs / "systemctl").write_text('#!/bin/sh\necho "systemctl $*" >>"$SYSTEMCTL_LOG"\n')
        (bin_stubs / "systemctl").chmod(0o755)
        self.appdir = appdir
        self.home = tmp / "home"
        self.data = tmp / "gg"
        (self.data / "client/app").mkdir(parents=True)
        (self.data / "client/app/package.json").write_text("{}")
        self.env = {k: v for k, v in os.environ.items() if not k.startswith(("APPIMAGE", "GG_LINUX"))}
        self.env.update(
            HOME=str(self.home), XDG_DATA_HOME=str(self.home / "data"), XDG_CONFIG_HOME=str(self.home / "config"),
            GG_LINUX_DATA=str(self.data), SYSTEMCTL_LOG=str(tmp / "systemctl.log"),
            PATH=str(bin_stubs) + os.pathsep + self.env["PATH"],
        )
        self.unit = self.home / "config/systemd/user/steelseries-ggd.service"
        self.entry = self.home / "data/applications/steelseries-gg.desktop"

    def run_apprun(self, appimage, *args):
        env = dict(self.env, APPIMAGE=appimage)
        r = subprocess.run([SHELL, str(self.appdir / "AppRun"), *args], env=env, capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)
        return r.stdout

    def test_integrate_quotes_the_path(self):
        appimage = str(self.tmp / 'My Apps/GG "new" $5 100%.AppImage')
        self.run_apprun(appimage, "--integrate", "service", "menu")
        unit = self.unit.read_text()
        self.assertIn('ExecStart="' + str(self.tmp) + '/My Apps/GG \\"new\\" $$5 100%%.AppImage" ggd\n', unit)
        entry = self.entry.read_text()
        # Escaped for the quoted argument, then as a desktop entry string.
        self.assertIn('Exec="' + str(self.tmp) + '/My Apps/GG \\\\"new\\\\" \\\\$5 100%%.AppImage"\n', entry)
        self.assertTrue((self.home / "data/icons/hicolor/scalable/apps/steelseries-gg.svg").exists())
        log = (self.tmp / "systemctl.log").read_text()
        self.assertIn("systemctl --user enable steelseries-ggd.service", log)

    def test_follows_a_moved_appimage(self):
        self.run_apprun(str(self.tmp / "old.AppImage"), "--integrate", "service", "menu")
        out = self.run_apprun(str(self.tmp / "new place.AppImage"))
        self.assertIn('ExecStart="' + str(self.tmp) + '/new place.AppImage" ggd', self.unit.read_text())
        self.assertIn('Exec="' + str(self.tmp) + '/new place.AppImage"', self.entry.read_text())
        # GG opens with ggd started as a run of its own.
        wrapper = self.data / "state/appimage-ggd"
        self.assertIn(f"launcher GG_LINUX_GGD={wrapper} ", out)
        self.assertIn("gg-setup --bootstrap", out)
        self.assertEqual(wrapper.read_text(), f"#!/bin/sh\nexec '{self.tmp}/new place.AppImage' ggd \"$@\"\n")

    def test_leaves_install_sh_files_alone(self):
        self.unit.parent.mkdir(parents=True)
        self.unit.write_text("[Service]\nExecStart=/home/me/.local/bin/ggd\n")
        self.entry.parent.mkdir(parents=True)
        self.entry.write_text("[Desktop Entry]\nExec=/home/me/.local/bin/steelseries-gg\n")
        self.run_apprun(str(self.tmp / "GG.AppImage"))
        self.assertEqual(self.unit.read_text(), "[Service]\nExecStart=/home/me/.local/bin/ggd\n")
        self.assertEqual(self.entry.read_text(), "[Desktop Entry]\nExec=/home/me/.local/bin/steelseries-gg\n")

    def test_ggd_opens_gg_through_the_appimage(self):
        out = self.run_apprun(str(self.tmp / "GG.AppImage"), "ggd", "--flag")
        self.assertEqual(out, f"ggd GG_LINUX_CLIENT={self.tmp}/GG.AppImage args=--flag\n")


if __name__ == "__main__":
    unittest.main()
