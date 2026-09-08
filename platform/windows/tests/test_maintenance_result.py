# SPDX-License-Identifier: Apache-2.0
"""Compile the production maintenance request against a faulting IPC channel."""
import io
import json
import os
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
            harness = '''#include <cassert>
#include <map>
#include "YunPinMaintenanceResult.h"
using DWORD = unsigned long;
using LRESULT = long;
constexpr int WEASEL_IPC_START_MAINTENANCE = 1;
constexpr int WEASEL_IPC_MAINTENANCE_IF_IDLE_V2 = 2;
struct PipeMessage { int message; int mode; int parameter; };
using namespace weasel;
struct Channel {
  bool fail = false; LRESULT reply = 0;
  LRESULT Transact(PipeMessage) { if (fail) throw DWORD(109); return reply; }
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
            subprocess.run([compiler, "-std=c++17", "-Wall", "-Wextra", "-Werror", "-include", "initializer_list",
                            "-I", str(target / "include"), str(target / "maintenance_test.cpp"),
                            "-o", str(target / "maintenance-test")], check=True)
            subprocess.run([str(target / "maintenance-test")], check=True)


if __name__ == "__main__":
    unittest.main()
