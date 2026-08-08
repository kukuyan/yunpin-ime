// SPDX-License-Identifier: Apache-2.0
//
// Test-only stand-in for librime's <rime/common.h>. It declares only the API
// surface that rime_yunpin_filter.cpp touches, so the filter can be compiled
// and exercised on a machine without librime, Boost or glog.
//
// Mirrors librime 1.17.0 (BSD-3-Clause), commit
// 33e78140250125871856cdc5b42ddc6a5fcd3cd4. When platform/upstream-lock.json
// moves librime, re-check these signatures against the real headers; the
// commit above is pinned by tools/importer/tests/test_platform_configs.py.
#pragma once
#include <filesystem>
#include <list>
#include <memory>
#include <set>
#include <sstream>
#include <string>
#include <utility>
#include <vector>
namespace rime {
using std::list; using std::set; using std::string; using std::vector;
using path = std::filesystem::path;
template <class T> using an = std::shared_ptr<T>;
template <class T> using of = std::shared_ptr<T>;
template <class T, class... A> an<T> New(A&&... a) {
  return std::make_shared<T>(std::forward<A>(a)...);
}
template <class T, class... A> class Class {};
struct NullLog { template <class T> NullLog& operator<<(const T&) { return *this; } };
}  // namespace rime
#define LOG(severity) ::rime::NullLog()
