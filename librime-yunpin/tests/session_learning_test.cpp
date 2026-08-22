// SPDX-License-Identifier: Apache-2.0
#include "yunpin/session_learning.hpp"

#include <chrono>
#include <iostream>
#include <stdexcept>
#include <string>
#include <vector>

namespace {

using yunpin::HabitQuery;
using yunpin::HabitStat;
using yunpin::LearningContext;
using yunpin::SessionCommit;
using yunpin::SessionLearning;

void Check(bool condition, const std::string& message) {
  if (!condition) {
    throw std::runtime_error(message);
  }
}

struct Harness {
  SessionLearning::TimePoint now{};
  std::string date_bucket{"2026-08-09"};
  SessionLearning learning{
      std::chrono::milliseconds(5000), [this] { return now; },
      [this] { return date_bucket; }};
};

void DeleteScalars(Harness* harness, std::size_t count) {
  for (std::size_t index = 0; index < count; ++index) {
    harness->learning.ObserveUnhandledKey(
        true, LearningContext::kNormal);
  }
}

const HabitStat* FindPhrase(const std::vector<HabitStat>& stats,
                            const std::string& phrase) {
  for (const HabitStat& stat : stats) {
    if (stat.phrase == phrase) {
      return &stat;
    }
  }
  return nullptr;
}

void TestGoldenCorrectionAndHabitStats() {
  Harness harness;
  harness.learning.ObserveCommit(
      SessionCommit{"日长", "ri chang", LearningContext::kNormal});
  harness.learning.ObserveComposition("", LearningContext::kNormal);
  DeleteScalars(&harness, 2);
  for (const std::string input : {"r", "ri", "ric", "richang"}) {
    harness.learning.ObserveComposition(input, LearningContext::kNormal);
  }
  harness.learning.ObserveCommit(
      SessionCommit{"日常", "ri chang", LearningContext::kNormal});

  Check(harness.learning.CorrectionScore("richang", "日长") == -1 &&
            harness.learning.CorrectionScore("ri chang", "日常") == 1,
        "日长 -> two Backspaces -> 日常 must produce negative/positive scores");
  const auto stats =
      harness.learning.QueryHabits(HabitQuery{"2026-08-09", true, 8});
  const HabitStat* wrong = FindPhrase(stats, "日长");
  const HabitStat* right = FindPhrase(stats, "日常");
  Check(wrong && wrong->selection_count == 1 &&
            wrong->corrected_from_count == 1,
        "old word must enter word-level corrected-from stats");
  Check(right && right->selection_count == 1 &&
            right->replacement_count == 1,
        "replacement must enter word-level positive stats");
}

void TestLatestOrdinarySelectionGetsNewerRankingOrder() {
  Harness harness;
  Check(harness.learning.ObserveCommit(
            SessionCommit{"办公是", "ban gong shi", LearningContext::kNormal}),
        "first ordinary homophone selection must be accepted");
  const std::uint64_t wrong =
      harness.learning.SelectionOrder("bangongshi", "办公是");
  Check(harness.learning.ObserveCommit(
            SessionCommit{"办公室", "ban gong shi", LearningContext::kNormal}),
        "replacement homophone selection must be accepted");
  const std::uint64_t right =
      harness.learning.SelectionOrder("bangongshi", "办公室");
  Check(wrong > 0 && right > wrong,
        "the latest ordinary homophone selection must have the newer order");
}

void TestBackspaceCountMustMatchUnicodeScalars() {
  Harness too_few;
  too_few.learning.ObserveCommit(
      SessionCommit{"日长", "richang", LearningContext::kNormal});
  DeleteScalars(&too_few, 1);
  too_few.learning.ObserveComposition("r", LearningContext::kNormal);
  too_few.learning.ObserveCommit(
      SessionCommit{"日常", "richang", LearningContext::kNormal});
  Check(too_few.learning.CorrectionScore("richang", "日常") == 0,
        "typing after too few Backspaces must break adjacency");

  Harness too_many;
  too_many.learning.ObserveCommit(
      SessionCommit{"日长", "richang", LearningContext::kNormal});
  DeleteScalars(&too_many, 3);
  too_many.learning.ObserveCommit(
      SessionCommit{"日常", "richang", LearningContext::kNormal});
  Check(too_many.learning.CorrectionScore("richang", "日常") == 0,
        "an extra Backspace must fail closed");

  Harness supplementary;
  supplementary.learning.ObserveCommit(
      SessionCommit{"𠀀日", "ceshi", LearningContext::kNormal});
  DeleteScalars(&supplementary, 2);
  supplementary.learning.ObserveComposition("ceshi",
                                            LearningContext::kNormal);
  supplementary.learning.ObserveCommit(
      SessionCommit{"测试", "ceshi", LearningContext::kNormal});
  Check(supplementary.learning.CorrectionScore("ceshi", "测试") == 1,
        "Backspace proof must count Unicode scalars rather than UTF-8 bytes");
}

void TestDifferentPinyinOtherKeyAbortAndTimeoutFailClosed() {
  Harness different;
  different.learning.ObserveCommit(
      SessionCommit{"日长", "richang", LearningContext::kNormal});
  DeleteScalars(&different, 2);
  different.learning.ObserveComposition("rili", LearningContext::kNormal);
  different.learning.ObserveCommit(
      SessionCommit{"日历", "rili", LearningContext::kNormal});
  Check(different.learning.CorrectionScore("rili", "日历") == 0,
        "different normalized pinyin must not learn a correction");

  Harness other_key;
  other_key.learning.ObserveCommit(
      SessionCommit{"日长", "richang", LearningContext::kNormal});
  other_key.learning.ObserveUnhandledKey(false, LearningContext::kNormal);
  DeleteScalars(&other_key, 2);
  other_key.learning.ObserveCommit(
      SessionCommit{"日常", "richang", LearningContext::kNormal});
  Check(other_key.learning.CorrectionScore("richang", "日常") == 0,
        "any non-Backspace unhandled key must break adjacency");

  Harness aborted;
  aborted.learning.ObserveCommit(
      SessionCommit{"日长", "richang", LearningContext::kNormal});
  DeleteScalars(&aborted, 2);
  aborted.learning.ObserveComposition("ri", LearningContext::kNormal);
  aborted.learning.ObserveComposition("", LearningContext::kNormal);
  aborted.learning.ObserveCommit(
      SessionCommit{"日常", "richang", LearningContext::kNormal});
  Check(aborted.learning.CorrectionScore("richang", "日常") == 0,
        "clearing a started replacement composition must break adjacency");

  Harness timed_out;
  timed_out.learning.ObserveCommit(
      SessionCommit{"日长", "richang", LearningContext::kNormal});
  timed_out.now += std::chrono::milliseconds(5001);
  DeleteScalars(&timed_out, 2);
  timed_out.learning.ObserveCommit(
      SessionCommit{"日常", "richang", LearningContext::kNormal});
  Check(timed_out.learning.CorrectionScore("richang", "日常") == 0,
        "the correction window must be a hard upper bound");
}

void TestSensitiveContextsNeverRecordOrBridgeChains() {
  for (const LearningContext context :
       {LearningContext::kPassword, LearningContext::kPrivate,
        LearningContext::kOneShot}) {
    Harness harness;
    harness.learning.ObserveCommit(SessionCommit{"隐私词", "yinsi", context});
    Check(harness.learning.QueryHabits().empty(),
          "sensitive context must not enter habit stats");
  }

  Harness interrupted;
  interrupted.learning.ObserveCommit(
      SessionCommit{"日长", "richang", LearningContext::kNormal});
  DeleteScalars(&interrupted, 2);
  interrupted.learning.ObserveComposition("ri", LearningContext::kPassword);
  interrupted.learning.ObserveCommit(
      SessionCommit{"日常", "richang", LearningContext::kNormal});
  Check(interrupted.learning.CorrectionScore("richang", "日常") == 0,
        "a sensitive context transition must sever the correction chain");
}

void TestHabitEntryLimitRejectsOnlyNewKeys() {
  Harness harness;
  for (std::size_t index = 0;
       index < SessionLearning::kMaxTrackedEntries; ++index) {
    const std::string suffix = std::to_string(index);
    harness.learning.ObserveCommit(
        SessionCommit{"词" + suffix, "ci", LearningContext::kNormal});
  }
  harness.learning.ObserveCommit(
      SessionCommit{"超限词", "chaoxian", LearningContext::kNormal});
  harness.date_bucket = "2026-08-10";
  harness.learning.ObserveCommit(
      SessionCommit{"词0", "ci", LearningContext::kNormal});
  harness.date_bucket = "2026-08-09";
  harness.learning.ObserveCommit(
      SessionCommit{"词0", "ci", LearningContext::kNormal});

  const auto stats = harness.learning.QueryHabits(
      HabitQuery{"2026-08-09", false,
                 SessionLearning::kMaxTrackedEntries + 1});
  Check(stats.size() == SessionLearning::kMaxTrackedEntries,
        "a session must never retain more than 50,000 word keys");
  const HabitStat* existing = FindPhrase(stats, "词0");
  Check(existing && existing->selection_count == 2,
        "an existing word must remain updatable after the entry cap");
  Check(FindPhrase(stats, "超限词") == nullptr,
        "a new word at the entry cap must fail closed without a record");
  Check(harness.learning.QueryHabits(
            HabitQuery{"2026-08-10", false, 8}).empty(),
        "a new date-bucket aggregate at the cap must also fail closed");
}

}  // namespace

int main() {
  try {
    TestGoldenCorrectionAndHabitStats();
    TestLatestOrdinarySelectionGetsNewerRankingOrder();
    TestBackspaceCountMustMatchUnicodeScalars();
    TestDifferentPinyinOtherKeyAbortAndTimeoutFailClosed();
    TestSensitiveContextsNeverRecordOrBridgeChains();
    TestHabitEntryLimitRejectsOnlyNewKeys();
    std::cout << "session_learning_tests: PASS\n";
    return 0;
  } catch (const std::exception& error) {
    std::cerr << "session_learning_tests: FAIL: " << error.what() << '\n';
    return 1;
  }
}
