#!/usr/bin/env python3
"""Test packaging/install.sh's desktop entry and user service, with the
build stubbed out."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

REPO = Path(__file__).resolve().parent.parent


class InstallTest(unittest.TestCase):
    def install(self, **variables):
        """Runs install.sh from a copy of packaging/ and returns the
        installed desktop entry, the service and the prefix."""
        tmp = Path(tempfile.mkdtemp(prefix="gg-install-"))
        self.addCleanup(shutil.rmtree, tmp)
        repo = tmp / "repo"
        shutil.copytree(REPO / "packaging", repo / "packaging")
        stubs = tmp / "stubs"
        stubs.mkdir()
        for name, body in {
            # go build -o bin/ ./cmd/...
            "go": 'mkdir -p bin && for b in ggd ggctl gg-setup; do : > "bin/$b"; done',
            "cc": "exit 1",
            "systemctl": "exit 0",
        }.items():
            (stubs / name).write_text("#!/bin/sh\n" + body + "\n")
            (stubs / name).chmod(0o755)
        home = tmp / "home"
        env = {k: v for k, v in os.environ.items() if k not in ("CONTAINER_ID", "DISTROBOX_ENTER_PATH")}
        env.update(
            HOME=str(home), PREFIX=str(home / ".local"),
            XDG_DATA_HOME=str(home / "data"), XDG_CONFIG_HOME=str(home / "config"),
            GG_LINUX_DATA=str(home / "gg"), GG_LINUX_SETUP="1",
            PATH=str(stubs) + os.pathsep + env["PATH"],
        )
        env.update(variables)
        result = subprocess.run(["sh", str(repo / "packaging/install.sh")], env=env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        desktop = (home / "data/applications/steelseries-gg.desktop").read_text()
        service = (home / "config/systemd/user/steelseries-ggd.service").read_text()
        return desktop, service, home / ".local"

    @staticmethod
    def line(text, key):
        return next(l for l in text.splitlines() if l.startswith(key + "="))

    def test_host(self):
        desktop, service, prefix = self.install()
        self.assertEqual(self.line(desktop, "Exec"), f"Exec={prefix}/bin/steelseries-gg")
        self.assertEqual(self.line(service, "ExecStart"), f"ExecStart={prefix}/bin/ggd")

    def test_distrobox(self):
        # The desktop entry and the service run on the host, where the
        # container's packages (Electron, ffmpeg, pactl...) are missing:
        # they enter the container installed from, as distrobox-export's
        # entries do.
        desktop, service, prefix = self.install(CONTAINER_ID="apps", DISTROBOX_ENTER_PATH="/usr/bin/distrobox-enter")
        self.assertEqual(self.line(desktop, "Exec"),
                         f"Exec=/usr/bin/distrobox-enter -n apps -- {prefix}/bin/steelseries-gg")
        self.assertEqual(self.line(service, "ExecStart"),
                         f"ExecStart=/usr/bin/distrobox-enter -n apps -- {prefix}/bin/ggd")


if __name__ == "__main__":
    unittest.main()
