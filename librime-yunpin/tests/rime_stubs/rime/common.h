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
#include <functional>
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
template <class X, class Y> an<X> As(const an<Y>& value) {
  return std::dynamic_pointer_cast<X>(value);
}
template <class T, class... A> an<T> New(A&&... a) {
  return std::make_shared<T>(std::forward<A>(a)...);
}
template <class T, class... A> class Class {};
class connection {
 public:
  connection() = default;
  explicit connection(std::shared_ptr<bool> active)
      : active_(std::move(active)) {}
  void disconnect() {
    if (active_) *active_ = false;
  }
 private:
  std::shared_ptr<bool> active_;
};
template <class Signature> class signal;
template <class... Args> class signal<void(Args...)> {
 public:
  template <class Callback> connection connect(Callback callback) {
    Slot slot;
    slot.active = std::make_shared<bool>(true);
    slot.callback = std::function<void(Args...)>(std::move(callback));
    slots_.push_back(slot);
    return connection(slot.active);
  }
  void operator()(Args... args) {
    for (const Slot& slot : slots_) {
      if (*slot.active) slot.callback(args...);
    }
  }
 private:
  struct Slot {
    std::shared_ptr<bool> active;
    std::function<void(Args...)> callback;
  };
  std::vector<Slot> slots_;
};
struct NullLog { template <class T> NullLog& operator<<(const T&) { return *this; } };
}  // namespace rime
#define LOG(severity) ::rime::NullLog()
