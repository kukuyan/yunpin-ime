// SPDX-License-Identifier: Apache-2.0
//
// Test-only stand-in for librime's <rime/schema.h>. It declares only the API
// surface that rime_yunpin_filter.cpp touches, so the filter can be compiled
// and exercised on a machine without librime, Boost or glog.
//
// Mirrors librime 1.17.0 (BSD-3-Clause), commit
// 33e78140250125871856cdc5b42ddc6a5fcd3cd4. When platform/upstream-lock.json
// moves librime, re-check these signatures against the real headers; the
// commit above is pinned by tools/importer/tests/test_platform_configs.py.
#pragma once
#include <rime/config/config_component.h>
namespace rime {
class Schema {
 public:
  Schema() = default;
  explicit Schema(Config* config) : config_(config) {}
  Config* config() const { return config_; }
  int page_size() const { return page_size_; }
  void set_page_size(int n) { page_size_ = n; }
 private:
  Config* config_ = nullptr; int page_size_ = 5;
};
}  // namespace rime
