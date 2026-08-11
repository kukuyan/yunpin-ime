// SPDX-License-Identifier: Apache-2.0
//
// Test-only stand-in for librime's <rime/filter.h>. It declares only the API
// surface that rime_yunpin_filter.cpp touches, so the filter can be compiled
// and exercised on a machine without librime, Boost or glog.
//
// Mirrors librime 1.17.0 (BSD-3-Clause), commit
// 33e78140250125871856cdc5b42ddc6a5fcd3cd4. When platform/upstream-lock.json
// moves librime, re-check these signatures against the real headers; the
// commit above is pinned by tools/importer/tests/test_platform_configs.py.
#pragma once
#include <rime/candidate.h>
#include <rime/ticket.h>
namespace rime {
class Engine; struct Segment; class Translation;
class Filter : public Class<Filter, const Ticket&> {
 public:
  explicit Filter(const Ticket& ticket)
      : engine_(ticket.engine), name_space_(ticket.name_space) {}
  virtual ~Filter() = default;
  virtual an<Translation> Apply(an<Translation> translation, CandidateList* candidates) = 0;
  virtual bool AppliesToSegment(Segment*) { return true; }
  string name_space() const { return name_space_; }
 protected:
  Engine* engine_; string name_space_;
};
}  // namespace rime
