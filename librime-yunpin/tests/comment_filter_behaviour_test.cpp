// SPDX-License-Identifier: Apache-2.0
// Portable behavior tests for the production candidate-comment filter.

#undef NDEBUG

#include "rime_yunpin_comment_filter.hpp"

#include <rime/candidate.h>
#include <rime/context.h>
#include <rime/engine.h>
#include <rime/translation.h>

#include <cassert>
#include <cstddef>
#include <iostream>
#include <string>
#include <utility>
#include <vector>

namespace {

using namespace rime;

class VectorTranslation : public Translation {
 public:
  explicit VectorTranslation(std::vector<an<Candidate>> candidates)
      : candidates_(std::move(candidates)) {
    set_exhausted(candidates_.empty());
  }

  bool Next() override {
    if (cursor_ < candidates_.size()) {
      ++cursor_;
    }
    set_exhausted(cursor_ >= candidates_.size());
    return !exhausted();
  }

  an<Candidate> Peek() override {
    return cursor_ < candidates_.size() ? candidates_[cursor_] : nullptr;
  }

 private:
  std::vector<an<Candidate>> candidates_;
  std::size_t cursor_{0};
};

struct Harness {
  Context context;
  Engine engine;

  Harness() { engine.context_ = &context; }

  Ticket ticket() {
    Ticket value;
    value.engine = &engine;
    value.name_space = "yunpin_comment_visibility";
    return value;
  }
};

an<SimpleCandidate> MakeCandidate(std::string text,
                                  std::string comment,
                                  std::string preedit = "") {
  auto candidate = New<SimpleCandidate>("phrase", 2, 9, text, comment,
                                        preedit);
  candidate->set_quality(7.25);
  return candidate;
}

void TestDefaultOffMasksOnlyPinyinAnnotation() {
  Harness harness;
  YunPinCommentFilter filter(harness.ticket());
  const auto pinyin = MakeCandidate("数据库", "［shu ju ku］", "shu ju ku");
  pinyin->set_correction(true);
  const auto note = MakeCandidate("数据", "纠错候选");
  const auto ascii_brackets = MakeCandidate("数目", "[shu mu]");
  const auto bracketed_note = MakeCandidate("数局", "［候选说明］");
  const auto tone_pinyin = MakeCandidate("数据", "［shù jù］");
  auto upstream = New<VectorTranslation>(
      std::vector<an<Candidate>>{pinyin, note, ascii_brackets, bracketed_note,
                                 tone_pinyin});

  CandidateList candidates;
  const auto filtered = filter.Apply(upstream, &candidates);
  assert(filtered != upstream);
  assert(filtered->Peek()->text() == "数据库");
  assert(filtered->Peek()->comment().empty());
  assert(filtered->Peek()->is_correction());
  assert(filtered->Next());
  assert(filtered->Peek() == note);
  assert(filtered->Peek()->comment() == "纠错候选");
  assert(filtered->Next());
  assert(filtered->Peek() == ascii_brackets);
  assert(filtered->Peek()->comment() == "[shu mu]");
  assert(filtered->Next());
  assert(filtered->Peek() == bracketed_note);
  assert(filtered->Peek()->comment() == "［候选说明］");
  assert(filtered->Next());
  assert(filtered->Peek()->text() == "数据");
  assert(filtered->Peek()->comment().empty());
}

void TestOnPassesTranslationThroughUnchanged() {
  Harness harness;
  harness.context.options_["yunpin_show_candidate_pinyin"] = true;
  YunPinCommentFilter filter(harness.ticket());
  const auto pinyin = MakeCandidate("数据库", "［shu ju ku］");
  auto upstream =
      New<VectorTranslation>(std::vector<an<Candidate>>{pinyin});

  CandidateList candidates;
  const auto filtered = filter.Apply(upstream, &candidates);
  assert(filtered == upstream);
  assert(filtered->Peek() == pinyin);
  assert(filtered->Peek()->comment() == "［shu ju ku］");
}

void TestExistingShadowsAreFlattenedWithoutVisibleChanges() {
  Harness harness;
  YunPinCommentFilter filter(harness.ticket());

  const auto genuine =
      MakeCandidate("数据库", "词典原注释", "shu ju ku preedit");
  const auto inner = New<ShadowCandidate>(
      genuine, "inner_type", "数据库（内）", "［shu ju ku］", false);
  const auto outer = New<ShadowCandidate>(
      inner, "visible_type", "数据库（可见）", "［shu ju ku］", false);
  outer->set_start(1);
  outer->set_end(11);
  outer->set_quality(19.5);
  outer->set_correction(true);
  auto upstream =
      New<VectorTranslation>(std::vector<an<Candidate>>{outer});

  CandidateList candidates;
  const auto filtered = filter.Apply(upstream, &candidates);
  const auto visible = filtered->Peek();
  assert(visible);
  assert(visible->type() == outer->type());
  assert(visible->text() == outer->text());
  assert(visible->comment().empty());
  assert(visible->start() == outer->start());
  assert(visible->end() == outer->end());
  assert(visible->quality() == outer->quality());
  assert(visible->preedit() == outer->preedit());
  assert(visible->is_correction() == outer->is_correction());

  const auto wrapper = As<ShadowCandidate>(visible);
  assert(wrapper);
  assert(wrapper->item() == genuine);
  assert(Candidate::GetGenuineCandidate(visible) == genuine);
}

void TestOrderAndExhaustionArePreserved() {
  Harness harness;
  YunPinCommentFilter filter(harness.ticket());
  const auto first = MakeCandidate("一", "［yi］");
  const auto second = MakeCandidate("二", "普通注释");
  const auto third = MakeCandidate("三", "［san］");
  auto upstream = New<VectorTranslation>(
      std::vector<an<Candidate>>{first, second, third});

  CandidateList candidates;
  const auto filtered = filter.Apply(upstream, &candidates);
  const std::vector<std::string> expected{"一", "二", "三"};
  std::vector<std::string> actual;
  while (!filtered->exhausted()) {
    const auto candidate = filtered->Peek();
    assert(candidate);
    actual.push_back(candidate->text());
    filtered->Next();
  }
  assert(actual == expected);
  assert(upstream->exhausted());
  assert(filtered->Peek() == nullptr);
  assert(!filtered->Next());

  auto empty_upstream =
      New<VectorTranslation>(std::vector<an<Candidate>>{});
  const auto empty = filter.Apply(empty_upstream, &candidates);
  assert(empty->exhausted());
  assert(empty->Peek() == nullptr);
  assert(!empty->Next());
}

}  // namespace

int main() {
  TestDefaultOffMasksOnlyPinyinAnnotation();
  TestOnPassesTranslationThroughUnchanged();
  TestExistingShadowsAreFlattenedWithoutVisibleChanges();
  TestOrderAndExhaustionArePreserved();
  std::cout << "yunpin comment filter behavior tests passed\n";
  return 0;
}
