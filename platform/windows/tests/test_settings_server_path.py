# SPDX-License-Identifier: Apache-2.0
from __future__ import annotations

import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[3]


class SettingsServerPathTest(unittest.TestCase):
    def test_actual_locked_menu_expression_resolves_from_server_directory(self) -> None:
        compiler = (
            (shutil.which("cl") if os.name == "nt" else None)
            or shutil.which("c++")
            or shutil.which("g++")
            or shutil.which("clang++")
        )
        if not compiler:
            self.skipTest("C++17 compiler is unavailable")
        source = Path(os.environ.get("YUNPIN_WEASEL_TEST_SOURCE", ROOT / "third_party/weasel"))
        archive = subprocess.run(
            ["git", "-C", str(source), "archive", "HEAD"],
            check=True, stdout=subprocess.PIPE, timeout=30,
        ).stdout
        lock = json.loads((ROOT / "platform/windows/dependencies.lock.json").read_text())
        with tempfile.TemporaryDirectory(prefix="yunpin-settings-path-") as temporary:
            target = Path(temporary)
            with tarfile.open(fileobj=io.BytesIO(archive), mode="r:") as stream:
                stream.extractall(target)
            for patch in lock["weasel"]["patches"]:
                subprocess.run(
                    ["git", "-c", "core.whitespace=cr-at-eol", "apply", "--ignore-space-change",
                     "--whitespace=error-all", str(ROOT / patch["path"])],
                    cwd=target, check=True, timeout=30,
                )
            server = (target / "WeaselServer/WeaselServerApp.cpp").read_text(encoding="utf-8-sig")
            match = re.search(r"std::filesystem::path yunpin_settings\s*=\s*(.*?);", server, re.S)
            self.assertIsNotNone(match, "the shipped server must contain the settings menu expression")
            expression = match.group(1)
            header = (target / "WeaselServer/WeaselServerApp.h").read_text(encoding="utf-8-sig")
            helper = re.search(r"static fs::path install_dir\(\)\s*\{(.*?)\n  \}", header, re.S)
            self.assertIsNotNone(helper, "the shipped server must contain its install directory helper")
            program = target / "path_check.cpp"
            program.write_text(
                "#include <filesystem>\n"
                "namespace fs = std::filesystem;\n"
                "#ifdef _WIN32\n"
                "#include <windows.h>\n"
                "static fs::path install_dir() {" + helper.group(1) + "}\n"
                "int wmain(int argc, wchar_t** argv) {\n"
                "  if (argc != 2) return 2;\n"
                "  auto dir = install_dir();\n"
                "#else\n"
                "int main(int argc, char** argv) {\n"
                "  if (argc != 2) return 2;\n"
                "  auto dir = fs::path(argv[0]).remove_filename();\n"
                "#endif\n"
                "  std::filesystem::path yunpin_settings = " + expression + ";\n"
                "  return yunpin_settings == std::filesystem::path(argv[1]) ? 0 : 1;\n"
                "}\n",
                encoding="utf-8",
            )
            binary = target / ("path_check.exe" if os.name == "nt" else "path_check")
            if Path(compiler).name.lower() in ("cl", "cl.exe"):
                command = [compiler, "/nologo", "/EHsc", "/std:c++17", f"/Fe:{binary}", str(program)]
            else:
                command = [compiler, "-std=c++17", str(program), "-o", str(binary)]
                if os.name == "nt":
                    command.insert(1, "-municode")
            # Cold Windows CI hosts can take over 30 seconds to compile and link
            # even this small filesystem harness. Keep a bounded failure while
            # allowing for runner startup and toolchain load.
            subprocess.run(command, cwd=target, check=True, timeout=120)
            for location in (target / "Preview", target / "custom install" / "Preview", target / "用户目录" / "Preview"):
                executable = location / "current" / "YunPinServer.exe"
                expected = location / "support" / "sync-agent" / "yunpin-settings.exe"
                with self.subTest(executable=executable):
                    executable.parent.mkdir(parents=True)
                    shutil.copy2(binary, executable)
                    subprocess.run([str(executable), str(expected)], check=True, timeout=10)


if __name__ == "__main__":
    unittest.main()
