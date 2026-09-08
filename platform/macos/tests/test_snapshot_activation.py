# SPDX-License-Identifier: Apache-2.0
"""Compile production activation boundaries using only synthetic fixtures."""
from pathlib import Path
import io
import json
import os
import subprocess
import tempfile
import tarfile
import unittest

ROOT = Path(__file__).resolve().parents[3]


class SnapshotActivationTests(unittest.TestCase):
    def test_filter_receipt_follows_real_index_replacement(self):
        source = Path(os.environ.get("YUNPIN_FILTER_TEST_SOURCE",
                      ROOT / "librime-yunpin/src/rime_yunpin_filter.cpp")).read_text()
        start = source.index("bool YunPinFilter::LoadSnapshot(")
        end = source.index("\n// This filter does three things", start)
        method = source[start:end]
        harness = r'''#include <cassert>
#include <array>
#include <filesystem>
#include <fstream>
#include <map>
#include <sstream>
#include <string>
#include "yunpin/snapshot_store.hpp"
#include "yunpin/snapshot_identity.hpp"
#define LOG(level) std::ostringstream()
namespace rime {
using path = std::filesystem::path;
struct Context {
  std::map<std::string, std::string> properties;
  void set_property(const std::string& key, const std::string& value) { properties[key] = value; }
};
struct Engine { Context ctx; Context* context() { return &ctx; } };
struct Deployer { path user_data_dir; };
struct Service {
  Deployer state;
  static Service& instance() { static Service service; return service; }
  Deployer& deployer() { return state; }
};
bool IsSafeRelativePath(const std::string& value) { return value == "private.tsv"; }
struct YunPinFilter {
  Engine engine;
  Engine* engine_ = &engine;
  yunpin::SnapshotStore store_;
  bool LoadSnapshot(const std::string&);
};
''' + method + r'''
}
int main(int argc, char** argv) {
  assert(argc == 2);
  assert(yunpin::SnapshotContentDigest("abc") ==
         "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
  assert(yunpin::SnapshotContentDigest("") ==
         "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855");
  rime::Service::instance().state.user_data_dir = argv[1];
  const auto file = std::filesystem::path(argv[1]) / "private.tsv";
  const std::string header = "phrase\tpinyin\tsource\tuse_count\tpinned\n";
  const std::string fixture = header + "云拼验收\tyun pin yan shou\tnative_selection\t9\ttrue\n";
  // The Python raw-string harness turns only these escaped fixture literals
  // into real separators below, keeping all other production bytes unchanged.
  auto decode = [](std::string value) {
    for (const auto& pair : {std::pair<std::string,std::string>{"\\t", "\t"}, {"\\n", "\n"}}) {
      std::size_t found = 0;
      while ((found = value.find(pair.first, found)) != std::string::npos)
        value.replace(found, pair.first.size(), pair.second);
    }
    return value;
  };
  const auto content = decode(fixture);
  std::ofstream(file) << content;
  rime::YunPinFilter filter;
  assert(filter.LoadSnapshot("private.tsv"));
  assert(filter.store_.Query("yunpinyanshou").size() == 1);
  assert(filter.engine.ctx.properties["yunpin_snapshot_applied_digest"] ==
         yunpin::SnapshotContentDigest(content));
  std::ofstream(file) << "invalid header";
  assert(!filter.LoadSnapshot("private.tsv"));
  assert(filter.store_.Query("yunpinyanshou").size() == 1);
  assert(filter.engine.ctx.properties["yunpin_snapshot_applied_digest"].empty());
  std::ofstream(file) << decode(header);
  assert(!filter.LoadSnapshot("private.tsv")); // no private rows is intentional
  assert(filter.store_.size() == 0);
  assert(filter.engine.ctx.properties["yunpin_snapshot_applied_digest"] ==
         yunpin::SnapshotContentDigest(decode(header)));
}
'''
        with tempfile.TemporaryDirectory(prefix="yunpin-activation-filter-") as directory:
            path = Path(directory)
            source_path = path / "filter.cpp"
            source_path.write_text(harness)
            subprocess.run(["xcrun", "clang++", "-std=c++17", "-Wall", "-Wextra", "-Werror",
                            "-I", str(ROOT / "librime-yunpin/include"), "-I", str(ROOT / "engine/include"),
                            str(source_path), str(ROOT / "librime-yunpin/src/snapshot_store.cpp"),
                            str(ROOT / "engine/src/phrase_engine.cpp"),
                            "-o", str(path / "filter-test")], check=True)
            subprocess.run([str(path / "filter-test"), directory], check=True)

    def test_host_preserves_old_sessions_until_loaded_and_idle(self):
        if os.environ.get("YUNPIN_SQUIRREL_TEST_SOURCE"):
            source = (Path(os.environ["YUNPIN_SQUIRREL_TEST_SOURCE"]) /
                      "sources/SquirrelApplicationDelegate.swift").read_text()
        else:
            archive = subprocess.check_output(["git", "-C", str(ROOT / "third_party/squirrel"), "archive", "HEAD"])
            with tempfile.TemporaryDirectory(prefix="yunpin-activation-source-") as directory:
                target = Path(directory)
                with tarfile.open(fileobj=io.BytesIO(archive)) as stream:
                    stream.extractall(target)
                lock = json.loads((ROOT / "platform/macos/dependencies.lock.json").read_text())
                for row in lock["squirrel_patches"]:
                    subprocess.run(["git", "apply", "--whitespace=error-all", str(ROOT / row["path"])],
                                   cwd=target, check=True)
                source = (target / "sources/SquirrelApplicationDelegate.swift").read_text()
        start = source.index("  private func clearAppliedSnapshotSession()")
        end = source.index("\n  func syncUserData(requestNonce:", start)
        methods = source[start:end]
        harness = r'''import Foundation
import Darwin
typealias RimeSessionId = UInt
class FakeAPI {
  var maintenance = false
  var createFails = false
  var schemaFails = false
  var digest = String(repeating: "b", count: 64)
  var destroyed: [UInt] = []
  var onGetProperty: (() -> Void)?
  func is_maintenance_mode() -> Bool { maintenance }
  func create_session() -> UInt { createFails ? 0 : 77 }
  func select_schema(_ session: UInt, _ schema: String) -> Bool { !schemaFails }
  func get_property(_ session: UInt, _ name: String, _ out: UnsafeMutablePointer<CChar>, _ size: Int) -> Bool {
    onGetProperty?()
    let bytes = Array(digest.utf8CString)
    guard bytes.count <= size else { return false }
    for index in bytes.indices { out[index] = bytes[index] }
    return true
  }
  func find_session(_ session: UInt) -> Bool { true }
  func destroy_session(_ session: UInt) -> Bool { destroyed.append(session); return true }
}
class SquirrelInputController {
  static var checks = 0
  static var busyAt = 0
  static var invalidations = 0
  static func prepareForUserDataMaintenance(invalidate: Bool = true) -> Bool {
    checks += 1
    if checks == busyAt { return false }
    if invalidate { invalidations += 1 }
    return true
  }
}
class Host {
  var request: [String]? = ["v1", String(repeating: "a", count: 32), "23", String(repeating: "b", count: 64)]
  let snapshotHostSession = String(repeating: "c", count: 32)
  let rimeAPI = FakeAPI()
  let rimeSyncStateLock = NSLock()
  var rimeSyncInFlight = false
  var appliedSnapshotSession: UInt = 41
  var snapshotProbeSession: UInt = 0
  var receipt = ""
  func snapshotReloadRequest(_ nonce: String) -> [String]? { request }
  func writeMaintenanceAcknowledgement(_ nonce: String, acknowledgementName: String, contents: String?) -> Bool {
    receipt = contents ?? ""
    return true
  }
''' + methods + r'''
}
for scenario in ["applied", "busy_before", "busy_after", "maintenance", "create", "schema", "digest", "expired"] {
  let host = Host()
  SquirrelInputController.checks = 0
  SquirrelInputController.invalidations = 0
  SquirrelInputController.busyAt = scenario == "busy_before" ? 1 : (scenario == "busy_after" ? 2 : 0)
  host.rimeAPI.maintenance = scenario == "maintenance"
  host.rimeAPI.createFails = scenario == "create"
  host.rimeAPI.schemaFails = scenario == "schema"
  host.rimeAPI.onGetProperty = {
    precondition(host.shouldIgnoreSnapshotProbeNotification(77))
    precondition(!host.shouldIgnoreSnapshotProbeNotification(41))
  }
  if scenario == "digest" { host.rimeAPI.digest = String(repeating: "d", count: 64) }
  if scenario == "expired" { host.rimeAPI.onGetProperty = { host.request = nil } }
  host.applyPrivateSnapshot(requestNonce: String(repeating: "a", count: 32))
  precondition(!host.shouldIgnoreSnapshotProbeNotification(77))
  if scenario == "applied" {
    precondition(host.receipt.hasSuffix("\tapplied\n"))
    precondition(host.appliedSnapshotSession == 77)
    precondition(host.rimeAPI.destroyed == [41])
    precondition(SquirrelInputController.invalidations == 1)
  } else {
    precondition(!host.receipt.hasSuffix("\tapplied\n"))
    precondition(host.appliedSnapshotSession == 41)
    precondition(!host.rimeAPI.destroyed.contains(41))
    precondition(SquirrelInputController.invalidations == 0)
  }
}
print("snapshot activation host lifecycle passed")
'''
        with tempfile.TemporaryDirectory(prefix="yunpin-activation-host-") as directory:
            path = Path(directory) / "host.swift"
            path.write_text(harness)
            subprocess.run(["xcrun", "swift", str(path)], check=True)


if __name__ == "__main__":
    unittest.main()
