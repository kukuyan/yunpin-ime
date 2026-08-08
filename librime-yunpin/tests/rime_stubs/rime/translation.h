// SPDX-License-Identifier: Apache-2.0
//
// Test-only stand-in for librime's <rime/translation.h>. It declares only the API
// surface that rime_yunpin_filter.cpp touches, so the filter can be compiled
// and exercised on a machine without librime, Boost or glog.
//
// Mirrors librime 1.17.0 (BSD-3-Clause), commit
// 33e78140250125871856cdc5b42ddc6a5fcd3cd4. When platform/upstream-lock.json
// moves librime, re-check these signatures against the real headers; the
// commit above is pinned by tools/importer/tests/test_platform_configs.py.
#pragma once
#include <rime/candidate.h>
namespace rime {
class Translation {
 public:
  Translation() = default;
  virtual ~Translation() = default;
  virtual bool Next() = 0;
  virtual an<Candidate> Peek() = 0;
  virtual int Compare(an<Translation>, const CandidateList&) { return 0; }
  bool exhausted() const { return exhausted_; }
 protected:
  void set_exhausted(bool e) { exhausted_ = e; }
 private:
  bool exhausted_ = false;
};
}  // namespace rime
