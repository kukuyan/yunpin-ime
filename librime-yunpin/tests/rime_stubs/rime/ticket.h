// SPDX-License-Identifier: Apache-2.0
//
// Test-only stand-in for librime's <rime/ticket.h>. It declares only the API
// surface that rime_yunpin_filter.cpp touches, so the filter can be compiled
// and exercised on a machine without librime, Boost or glog.
//
// Mirrors librime 1.17.0 (BSD-3-Clause), commit
// 33e78140250125871856cdc5b42ddc6a5fcd3cd4. When platform/upstream-lock.json
// moves librime, re-check these signatures against the real headers; the
// commit above is pinned by tools/importer/tests/test_platform_configs.py.
#pragma once
#include <rime/common.h>
namespace rime {
class Engine; class Schema;
struct Ticket { Engine* engine = nullptr; Schema* schema = nullptr; string name_space; string klass; };
}  // namespace rime
