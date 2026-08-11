// SPDX-License-Identifier: Apache-2.0
//
// Test-only stand-in for librime's <rime/config/config_component.h>. It declares only the API
// surface that rime_yunpin_filter.cpp touches, so the filter can be compiled
// and exercised on a machine without librime, Boost or glog.
//
// Mirrors librime 1.17.0 (BSD-3-Clause), commit
// 33e78140250125871856cdc5b42ddc6a5fcd3cd4. When platform/upstream-lock.json
// moves librime, re-check these signatures against the real headers; the
// commit above is pinned by tools/importer/tests/test_platform_configs.py.
#pragma once
#include <map>
#include <rime/common.h>
namespace rime {
// Backed by a plain map so a test can drive the exact keys the filter reads.
class Config {
 public:
  bool GetBool(const string& p, bool* v) {
    auto it = bools_.find(p); if (it == bools_.end()) return false; *v = it->second; return true;
  }
  bool GetInt(const string& p, int* v) {
    auto it = ints_.find(p); if (it == ints_.end()) return false; *v = it->second; return true;
  }
  bool GetString(const string& p, string* v) {
    auto it = strings_.find(p); if (it == strings_.end()) return false; *v = it->second; return true;
  }
  std::map<string, bool> bools_; std::map<string, int> ints_; std::map<string, string> strings_;
};
}  // namespace rime
