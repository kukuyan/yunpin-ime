# SPDX-License-Identifier: Apache-2.0
"""Compile the production maintenance request against a faulting IPC channel."""
import io
import json
import os
import re
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]


class MaintenanceResultTests(unittest.TestCase):
    def test_connected_transport_failure_is_not_composing(self):
        compiler = shutil.which("c++")
        if not compiler:
            self.skipTest("portable C++ compiler unavailable")
        source = Path(os.environ.get("YUNPIN_WEASEL_TEST_SOURCE", ROOT / "third_party/weasel"))
        archive = subprocess.check_output(["git", "-C", str(source), "archive", "HEAD"])
        with tempfile.TemporaryDirectory(prefix="yunpin-maintenance-result-") as directory:
            target = Path(directory)
            with tarfile.open(fileobj=io.BytesIO(archive)) as stream:
                stream.extractall(target)
            lock = json.loads((ROOT / "platform/windows/dependencies.lock.json").read_text())
            for row in lock["weasel"]["patches"]:
                if "0011-" in row["path"]:
                    legacy_server = (target / "WeaselIPCServer/WeaselServerImpl.cpp").read_text(encoding="utf-8-sig")
                subprocess.run(["git", "-c", "core.whitespace=cr-at-eol", "apply", "--ignore-space-change",
                                "--whitespace=error-all", str(ROOT / row["path"])], cwd=target, check=True)
            implementation = (target / "WeaselIPC/WeaselClientImpl.cpp").read_text(encoding="utf-8-sig")
            start = implementation.index("MaintenanceResult ClientImpl::TryStartMaintenanceResult()")
            end = implementation.index("\nvoid ClientImpl::EndMaintenance()", start)
            body = implementation[start:end]
            server = (target / "RimeWithWeasel/RimeWithWeasel.cpp").read_text(encoding="utf-8-sig")
            start = server.index("weasel::MaintenanceResult RimeWithWeaselHandler::_MaintenanceReadiness()")
            end = server.index("\nbool RimeWithWeaselHandler::TryStartMaintenance()", start)
            readiness = server[start:end]
            ipc = (target / "include/WeaselIPC.h").read_text(encoding="utf-8-sig")
            command_start = ipc.index("enum WEASEL_IPC_COMMAND {")
            commands = ipc[command_start:ipc.index("};", command_start) + 2]
            # Compile the real legacy dispatcher, not a simulated switch. An
            # unknown command must not reach any old maintenance handler.
            begin = legacy_server.index("#define MAP_PIPE_MSG_HANDLE")
            finish = legacy_server.index("\nPipeServer::PipeServer", begin)
            dispatch = legacy_server[begin:finish].replace("ServerImpl::HandlePipeMessage", "LegacyServer::HandlePipeMessage")
            handlers = set(re.findall(r"PIPE_MSG_HANDLE\(WEASEL_[A-Z_]+,\s*(\w+)\)", dispatch))
            stub_handlers = "\n".join(
                f"DWORD {name}(WEASEL_IPC_COMMAND, DWORD, DWORD) {{ ++calls; return 1; }}"
                for name in sorted(handlers))
            legacy = ("struct LegacyServer { int calls = 0; " + stub_handlers +
                      " template <typename T> void HandlePipeMessage(PipeMessage, T); };\n" +
                      dispatch + "\n")
            harness = '''#include <cassert>
#include <map>
#include "YunPinMaintenanceResult.h"
using DWORD = unsigned long;
using LRESULT = long;
constexpr int WM_APP = 0x8000;
''' + commands + '''
[[maybe_unused]] constexpr int WEASEL_IPC_MAINTENANCE_IF_IDLE_V2 = 2;
struct PipeMessage { WEASEL_IPC_COMMAND Msg; DWORD wParam; DWORD lParam; };
using namespace weasel;
''' + legacy + '''
struct Channel {
  LegacyServer legacy;
  bool old_host = false;
  bool fail = false; LRESULT reply = 0;
  LRESULT Transact(PipeMessage request) {
    if (fail) throw DWORD(109);
    if (old_host) {
      LRESULT value = 0;
      legacy.HandlePipeMessage(request, [&](DWORD result) { value = result; });
      return value;
    }
    return reply;
  }
};
struct ClientImpl {
  Channel channel; int session_id = 42;
  MaintenanceResult TryStartMaintenanceResult();
};
using RimeSessionId = int;
struct RimeStatus { bool is_composing = false; };
#define RIME_STRUCT(type, variable) type variable
struct RimeApi {
  bool found = true; bool inspectable = true; bool composing = false;
  bool find_session(int) { return found; }
  bool get_status(int, RimeStatus* status) { status->is_composing = composing; return inspectable; }
  void free_status(RimeStatus*) {}
};
RimeApi api;
RimeApi* rime_api = &api;
struct SessionStatus { int session_id = 1; };
struct RimeWithWeaselHandler {
  bool m_disabled = false;
  int m_active_session = 1;
  std::map<int, SessionStatus> m_session_status_map{{1, SessionStatus{}}};
  MaintenanceResult _MaintenanceReadiness();
};
''' + body + readiness + '''
int main() {
  ClientImpl client;
  client.channel.old_host = true;
  assert(client.TryStartMaintenanceResult() == MaintenanceResult::ProtocolError);
  assert(client.channel.legacy.calls == 0);
  assert(client.session_id == 42);
  client.channel.old_host = false;
  client.channel.fail = true;
  assert(client.TryStartMaintenanceResult() == MaintenanceResult::Unavailable);
  assert(client.session_id == 42);
  client.channel.fail = false;
  for (auto reply : {0L, 1L, 2L, 999L}) {
    client.channel.reply = reply;
    assert(client.TryStartMaintenanceResult() == MaintenanceResult::ProtocolError);
    assert(client.session_id == 42);
  }
  client.channel.reply = static_cast<LRESULT>(MaintenanceResult::ComposingBusy);
  assert(client.TryStartMaintenanceResult() == MaintenanceResult::ComposingBusy);
  assert(client.session_id == 42);
  client.channel.reply = static_cast<LRESULT>(MaintenanceResult::Accepted);
  assert(client.TryStartMaintenanceResult() == MaintenanceResult::Accepted);
  assert(client.session_id == 0);
  RimeWithWeaselHandler server;
  api.inspectable = false;
  assert(server._MaintenanceReadiness() == MaintenanceResult::ProtocolError);
  assert(server.m_session_status_map.size() == 1);
  api.inspectable = true;
  api.composing = true;
  assert(server._MaintenanceReadiness() == MaintenanceResult::ComposingBusy);
  assert(server.m_session_status_map.size() == 1);
  api.composing = false;
  assert(server._MaintenanceReadiness() == MaintenanceResult::Accepted);
  api.found = false;
  assert(server._MaintenanceReadiness() == MaintenanceResult::Accepted);
  assert(server.m_session_status_map.empty());
  assert(server.m_active_session == 0);
}
'''
            (target / "maintenance_test.cpp").write_text(harness)
            subprocess.run([compiler, "-std=c++17", "-Wall", "-Wextra", "-Werror", "-Wno-switch", "-include", "initializer_list",
                            "-I", str(target / "include"), str(target / "maintenance_test.cpp"),
                            "-o", str(target / "maintenance-test")], check=True)
            subprocess.run([str(target / "maintenance-test")], check=True)


if __name__ == "__main__":
    unittest.main()
