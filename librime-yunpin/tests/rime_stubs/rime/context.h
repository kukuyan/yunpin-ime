// SPDX-License-Identifier: Apache-2.0
//
// Test-only stand-in for librime's <rime/context.h>. It declares only the API
// surface that rime_yunpin_filter.cpp touches, so the filter can be compiled
// and exercised on a machine without librime, Boost or glog.
//
// Mirrors librime 1.17.0 (BSD-3-Clause), commit
// 33e78140250125871856cdc5b42ddc6a5fcd3cd4. When platform/upstream-lock.json
// moves librime, re-check these signatures against the real headers; the
// commit above is pinned by tools/importer/tests/test_platform_configs.py.
#pragma once
#include <map>
#include <rime/candidate.h>
#include <rime/common.h>
#include <rime/composition.h>
#include <rime/key_event.h>
namespace rime {
class Context {
 public:
  using Notifier = signal<void(Context*)>;
  using OptionUpdateNotifier = signal<void(Context*, const string&)>;
  using KeyEventNotifier = signal<void(Context*, const KeyEvent&)>;
  const string& input() const { return input_; }
  string GetCommitText() const { return commit_text_; }
  an<Candidate> GetSelectedCandidate() const {
    return composition_.empty() ? nullptr
                                : composition_.back().GetSelectedCandidate();
  }
  Composition& composition() { return composition_; }
  const Composition& composition() const { return composition_; }
  bool get_option(const string& name) const {
    auto it = options_.find(name); return it != options_.end() && it->second;
  }
  Notifier& commit_notifier() { return commit_notifier_; }
  Notifier& update_notifier() { return update_notifier_; }
  Notifier& delete_notifier() { return delete_notifier_; }
  OptionUpdateNotifier& option_update_notifier() {
    return option_update_notifier_;
  }
  KeyEventNotifier& unhandled_key_notifier() {
    return unhandled_key_notifier_;
  }
  string input_;
  string commit_text_;
  Composition composition_;
  std::map<string, bool> options_;
 private:
  Notifier commit_notifier_;
  Notifier update_notifier_;
  Notifier delete_notifier_;
  OptionUpdateNotifier option_update_notifier_;
  KeyEventNotifier unhandled_key_notifier_;
};
}  // namespace rime
