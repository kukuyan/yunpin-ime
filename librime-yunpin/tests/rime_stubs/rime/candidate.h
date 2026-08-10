// SPDX-License-Identifier: Apache-2.0
//
// Test-only stand-in for librime's <rime/candidate.h>. It declares only the API
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
class Candidate {
 public:
  Candidate() = default;
  Candidate(const string& type, size_t start, size_t end, double quality = 0.)
      : type_(type), start_(start), end_(end), quality_(quality) {}
  virtual ~Candidate() = default;
  static an<Candidate> GetGenuineCandidate(const an<Candidate>& candidate) {
    return candidate;
  }
  const string& type() const { return type_; }
  size_t start() const { return start_; }
  size_t end() const { return end_; }
  double quality() const { return quality_; }
  bool is_correction() const { return is_correction_; }
  void set_correction(bool correction) { is_correction_ = correction; }
  virtual const string& text() const = 0;
  virtual string comment() const { return string(); }
  virtual string preedit() const { return string(); }
 private:
  string type_;
  size_t start_ = 0;
  size_t end_ = 0;
  bool is_correction_ = false;
  double quality_ = 0.;
};
using CandidateList = vector<of<Candidate>>;
class SimpleCandidate : public Candidate {
 public:
  SimpleCandidate() = default;
  SimpleCandidate(const string& type, size_t start, size_t end,
                  const string& text, const string& comment = string(),
                  const string& preedit = string())
      : Candidate(type, start, end), text_(text), comment_(comment), preedit_(preedit) {}
  const string& text() const override { return text_; }
  string comment() const override { return comment_; }
  string preedit() const override { return preedit_; }
 private:
  string text_, comment_, preedit_;
};
}  // namespace rime
