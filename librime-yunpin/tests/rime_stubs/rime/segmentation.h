// SPDX-License-Identifier: Apache-2.0
//
// Test-only stand-in for librime's <rime/segmentation.h>. It declares only the API
// surface that rime_yunpin_filter.cpp touches, so the filter can be compiled
// and exercised on a machine without librime, Boost or glog.
//
// Mirrors librime 1.17.0 (BSD-3-Clause), commit
// 33e78140250125871856cdc5b42ddc6a5fcd3cd4. When platform/upstream-lock.json
// moves librime, re-check these signatures against the real headers; the
// commit above is pinned by tools/importer/tests/test_platform_configs.py.
#pragma once
#include <rime/candidate.h>
#include <rime/common.h>
namespace rime {
struct Segment {
  enum Status { kVoid, kGuess, kSelected, kConfirmed };
  Status status = kVoid;
  size_t start = 0, end = 0, length = 0;
  set<string> tags;
  size_t selected_index = 0;
  string prompt;
  Segment() = default;
  Segment(int s, int e) : start(s), end(e), length(e - s) {}
  bool HasTag(const string& tag) const { return tags.find(tag) != tags.end(); }
  an<Candidate> GetSelectedCandidate() const { return selected_candidate_; }
  an<Candidate> selected_candidate_;
};
}  // namespace rime
