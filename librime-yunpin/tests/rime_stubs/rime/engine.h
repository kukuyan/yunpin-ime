// SPDX-License-Identifier: Apache-2.0
//
// Test-only stand-in for librime's <rime/engine.h>. It declares only the API
// surface that rime_yunpin_filter.cpp touches, so the filter can be compiled
// and exercised on a machine without librime, Boost or glog.
//
// Mirrors librime 1.17.0 (BSD-3-Clause), commit
// 33e78140250125871856cdc5b42ddc6a5fcd3cd4. When platform/upstream-lock.json
// moves librime, re-check these signatures against the real headers; the
// commit above is pinned by tools/importer/tests/test_platform_configs.py.
#pragma once
#include <rime/context.h>
#include <rime/schema.h>
namespace rime {
class Engine {
 public:
  Schema* schema() const { return schema_; }
  Context* context() const { return context_; }
  Schema* schema_ = nullptr; Context* context_ = nullptr;
};
}  // namespace rime
