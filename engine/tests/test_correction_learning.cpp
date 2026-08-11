// SPDX-License-Identifier: Apache-2.0
#include "yunpin/correction_learning.hpp"
#include "yunpin/phrase_engine.hpp"

#include <algorithm>
#include <iostream>
#include <sstream>
#include <stdexcept>
#include <string>
#include <utility>
#include <vector>

namespace {

using yunpin::Candidate;
using yunpin::CorrectionLearning;
using yunpin::HabitQuery;
using yunpin::HabitStat;
using yunpin::LearningContext;
using yunpin::PhraseEntry;
using yunpin::PhraseIndex;
using yunpin::PhraseOrigin;
using yunpin::SelectionEvent;

void Check(bool condition, const std::string& message) {
  if (!condition) {
    throw std::runtime_error(message);
  }
}

PhraseEntry Entry(std::string id, std::string text, std::string pinyin,
                  std::int64_t weight) {
  PhraseEntry entry;
  entry.id = std::move(id);
  entry.text = std::move(text);
  entry.syllables = yunpin::SplitPinyin(pinyin);
  entry.origin = PhraseOrigin::kPublic;
  entry.static_weight = weight;
  return entry;
}

const HabitStat* FindStat(const std::vector<HabitStat>& stats,
                          const std::string& phrase) {
  const auto found =
      std::find_if(stats.begin(), stats.end(), [&](const HabitStat& stat) {
        return stat.phrase == phrase;
      });
  return found == stats.end() ? nullptr : &*found;
}

void TestImmediateCorrectionChangesRankingAndInvalidatesCache() {
  PhraseIndex index({Entry("wrong", "日长", "ri chang", 101),
                     Entry("right", "日常", "ri chang", 100)});
  const auto before = index.Query("richang");
  Check(!before.empty() && before.front().id == "wrong",
        "golden setup must reproduce the stale wrong candidate first");

  CorrectionLearning learning;
  const std::uint64_t cached_epoch = learning.candidate_epoch();
  const std::uint32_t cached_revision = index.revision();
  const auto first = learning.RecordSelection(
      SelectionEvent{"2026-08-09", "wrong", "日长", "ri chang"});
  Check(first.recorded && !first.correction_completed,
        "an ordinary selection must be monitored without inventing a correction");
  Check(learning.UndoLastSelection(),
        "undo must arm only the just-committed entry");

  const auto correction = learning.RecordSelection(
      SelectionEvent{"2026-08-09", "right", "日常", "ri chang"});
  Check(correction.recorded && correction.correction_completed &&
            correction.requires_requery && correction.feedback.size() == 2,
        "日长 -> undo -> 日常 must emit a bounded correction and requery signal");
  Check(!learning.CanReuseCandidateEpoch(cached_epoch),
        "a menu cached before correction must be stale");

  for (const auto& feedback : correction.feedback) {
    Check(index.ApplyCorrectionFeedback(feedback.entry_id, feedback.delta),
          "feedback must target a known candidate");
  }
  Check(!index.CanReuseRevision(cached_revision),
        "the phrase index revision must invalidate a stale composition cache");
  const auto after = index.Query("richang");
  Check(!after.empty() && after.front().id == "right",
        "explicit negative/positive feedback must rerank 日常 ahead of 日长");

  const auto report = learning.Query(HabitQuery{"2026-08-09", true, 10});
  const HabitStat* wrong = FindStat(report, "日长");
  const HabitStat* right = FindStat(report, "日常");
  Check(wrong != nullptr && wrong->selection_count == 1 &&
            wrong->corrected_from_count == 1 &&
            wrong->net_correction_feedback() == -1,
        "the old word must retain one selection plus one explicit correction");
  Check(right != nullptr && right->selection_count == 1 &&
            right->replacement_count == 1 &&
            right->net_correction_feedback() == 1,
        "the replacement word must receive positive correction feedback");
}

void TestAdjacencyAndPrivacyBoundaries() {
  CorrectionLearning learning;
  Check(learning.RecordSelection(
            SelectionEvent{"2026-08-09", "old", "日长", "ri chang"})
            .recorded,
        "normal word selection should record");
  Check(learning.UndoLastSelection(), "normal undo should arm correction");
  learning.BreakAdjacency();
  Check(!learning.RecordSelection(
             SelectionEvent{"2026-08-09", "new", "日常", "ri chang"})
             .correction_completed,
        "an unrelated edit must break the correction chain");

  for (const LearningContext context :
       {LearningContext::kPrivate, LearningContext::kPassword,
        LearningContext::kOneShot}) {
    const auto ignored = learning.RecordSelection(SelectionEvent{
        "2026-08-09", "private", "隐私词", "yin si ci", context});
    Check(!ignored.recorded && !ignored.correction_completed,
          "private/password/one-shot selections must not enter monitoring");
  }

  Check(learning.RecordSelection(
            SelectionEvent{"2026-08-09", "before-private", "旧词", "jiu ci"})
            .recorded,
        "normal setup selection should record");
  Check(learning.UndoLastSelection(), "normal setup undo should arm correction");
  (void)learning.RecordSelection(SelectionEvent{
      "2026-08-09", "password", "密码词", "mi ma ci",
      LearningContext::kPassword});
  Check(!learning.RecordSelection(
             SelectionEvent{"2026-08-09", "after-private", "新词", "xin ci"})
             .correction_completed,
        "private input must sever association with later normal input");

  SelectionEvent opted_out{"2026-08-09", "opted-out", "普通词", "pu tong ci"};
  opted_out.monitorable = false;
  Check(!learning.RecordSelection(opted_out).recorded,
        "host-sensitive entries must be suppressible without inspecting text");
  Check(!learning.RecordSelection(SelectionEvent{
             "2026-08-09", "email", "person@example.com", "email"})
             .recorded,
        "obvious sensitive values must be filtered locally");
}

void TestAggregateTsvRoundTripAndSensitiveReadFilter() {
  CorrectionLearning learning;
  (void)learning.RecordSelection(
      SelectionEvent{"2026-08-09", "right", "日常", "ri chang"});
  const auto stats = learning.Query();

  std::stringstream encoded;
  yunpin::ExportHabitReportTsv(encoded, stats);
  const auto decoded = yunpin::ParseHabitReportTsv(encoded);
  Check(decoded.size() == 1 && decoded.front().phrase == "日常" &&
            decoded.front().pinyin == "richang" &&
            decoded.front().selection_count == 1,
        "word-level daily aggregates must survive a local report round trip");

  std::stringstream hostile;
  hostile << "date\tentry_id\tphrase\tpinyin\tselections\tcorrected_from\treplacements\n"
          << "2026-08-09\tsafe\t日常\trichang\t2\t0\t1\n"
          << "2026-08-09\tleak\tperson@example.com\temail\t9\t9\t9\n";
  const auto filtered = yunpin::ParseHabitReportTsv(hostile);
  Check(filtered.size() == 1 && filtered.front().phrase == "日常",
        "the monitor reader must not surface obvious sensitive entries");
}

void TestTombstoneAlsoInvalidatesCandidateRevision() {
  PhraseIndex index({Entry("deleted", "旧词", "jiu ci", 1)});
  const std::uint32_t revision = index.revision();
  Check(index.ApplyTombstone("deleted"), "known word must accept tombstone");
  Check(!index.CanReuseRevision(revision),
        "deleting a candidate must invalidate a cached composition menu");
}

void TestFiftyThousandEntryAggregationUsesStableIndex() {
  CorrectionLearning learning;
  constexpr std::size_t kEntryCount = 50000;
  for (std::size_t index = 0; index < kEntryCount; ++index) {
    const std::string suffix = std::to_string(index);
    const auto update = learning.RecordSelection(SelectionEvent{
        "2026-08-09", "entry-" + suffix, "词" + suffix, "ci"});
    Check(update.recorded, "synthetic monitor word must remain eligible");
  }
  const auto report = learning.Query(HabitQuery{"2026-08-09", false, 1});
  Check(report.size() == 1 && report.front().selection_count == 1,
        "50,000 word aggregates must remain queryable through the keyed index");

  const auto rejected = learning.RecordSelection(SelectionEvent{
      "2026-08-09", "entry-over-cap", "新词", "xin ci"});
  Check(!rejected.recorded && !rejected.correction_completed,
        "a new aggregate beyond 50,000 entries must fail closed");

  const auto existing = learning.RecordSelection(
      SelectionEvent{"2026-08-09", "entry-0", "词0", "ci"});
  Check(existing.recorded,
        "an existing aggregate must remain updateable at the 50,000-entry cap");
  const auto existing_report =
      learning.Query(HabitQuery{"2026-08-09", false, kEntryCount});
  const HabitStat* first = FindStat(existing_report, "词0");
  Check(existing_report.size() == kEntryCount && first != nullptr &&
            first->selection_count == 2,
        "the cap must bound cardinality without freezing existing counters");
}

}  // namespace

int main() {
  try {
    TestImmediateCorrectionChangesRankingAndInvalidatesCache();
    TestAdjacencyAndPrivacyBoundaries();
    TestAggregateTsvRoundTripAndSensitiveReadFilter();
    TestTombstoneAlsoInvalidatesCandidateRevision();
    TestFiftyThousandEntryAggregationUsesStableIndex();
    std::cout << "correction_learning_tests: PASS\n";
    return 0;
  } catch (const std::exception& error) {
    std::cerr << "correction_learning_tests: FAIL: " << error.what() << '\n';
    return 1;
  }
}
