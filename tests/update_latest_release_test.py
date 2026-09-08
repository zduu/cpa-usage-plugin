"""Exercise the updater with a mapped plugin and fake network/Docker commands."""
import mmap
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / "scripts/update-latest-release.sh"


class UpdateReleaseTest(unittest.TestCase):
    def test_atomic_install_and_restart_retry(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            binaries = root / "bin"
            binaries.mkdir()
            plugin_dir = root / "plugins"
            plugin_dir.mkdir()
            plugin = plugin_dir / "usage-dashboard-zduu.so"
            plugin.write_bytes(b"old plugin image")
            config = root / "config.yaml"
            config.write_text("plugins:\n  usage-dashboard-zduu:\n    update_enabled: true\n    update_version: v9.9.9\n")
            fake_commands = {
                "curl": '#!/bin/sh\nwhile [ "$#" -gt 0 ]; do\nif [ "$1" = "-o" ]; then shift; printf "new plugin image" > "$1"; exit 0; fi\nshift\ndone\nprintf \'{"tag_name":"v9.9.9"}\\n\'\n',
                "file": '#!/bin/sh\nprintf "ELF 64-bit x86-64\\n"\n',
                "docker": '#!/bin/sh\necho restart >> "$RESTART_LOG"\nexit "${RESTART_EXIT:-0}"\n',
            }
            for name, source in fake_commands.items():
                command = binaries / name
                command.write_text(source)
                command.chmod(0o755)
            log = root / "restarts"
            env = dict(os.environ, PATH=str(binaries) + os.pathsep + os.environ["PATH"],
                       CONFIG_FILE=str(config), PLUGIN_DIR=str(plugin_dir),
                       STATE_DIR=str(plugin_dir), PLUGIN_PLATFORM="linux-amd64",
                       RESTART_LOG=str(log), RESTART_EXIT="7")
            with plugin.open("rb") as old_file, mmap.mmap(old_file.fileno(), 0, access=mmap.ACCESS_READ) as loaded:
                before_inode = plugin.stat().st_ino
                result = subprocess.run(["sh", str(SCRIPT), "--restart"], env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn("docker restart failed", result.stdout)
                self.assertNotEqual(before_inode, plugin.stat().st_ino)
                self.assertEqual(loaded[:], b"old plugin image")
                self.assertEqual(plugin.read_bytes(), b"new plugin image")
            self.assertEqual(len(list(plugin_dir.glob("*.bak-*"))), 1)
            self.assertFalse(list(plugin_dir.glob(".usage-dashboard-zduu-install.*")))
            env["RESTART_EXIT"] = "0"
            result = subprocess.run(["sh", str(SCRIPT), "--restart"], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(log.read_text().splitlines(), ["restart", "restart"])


if __name__ == "__main__":
    unittest.main()
