// SPDX-License-Identifier: Apache-2.0
#pragma once

#include <chrono>
#include <cstdint>
#include <functional>
#include <mutex>
#include <optional>
#include <string>
#include <string_view>
#include <unordered_map>
#include <unordered_set>
#include <vector>

#include "yunpin/correction_learning.hpp"

namespace yunpin {

struct SessionCommit {
  std::string phrase;
  std::string pinyin;
  LearningContext context{LearningContext::kNormal};
};

// Fail-closed session state for a correction that can be proven from the
// librime event stream: one entry commit, exactly one unmodified Backspace per
// Unicode scalar, then a different entry committed from the same normalized
// pinyin within a short window. It stores no surrounding sentence or host
// metadata and performs no file or network operation.
class SessionLearning {
 public:
  static constexpr std::size_t kMaxTrackedEntries = 50000;
  using TimePoint = std::chrono::steady_clock::time_point;
  using Clock = std::function<TimePoint()>;
  using DateBucketProvider = std::function<std::string()>;

  explicit SessionLearning(
      std::chrono::milliseconds correction_window =
          std::chrono::milliseconds(5000),
      Clock clock = {}, DateBucketProvider date_bucket_provider = {});

  void ObserveCommit(SessionCommit commit);
  void ObserveUnhandledKey(bool is_unmodified_backspace,
                           LearningContext context);
  void ObserveComposition(std::string_view input, LearningContext context);
  void BreakAdjacency();

  [[nodiscard]] std::int32_t CorrectionScore(
      std::string_view pinyin, std::string_view phrase) const;
  [[nodiscard]] std::vector<HabitStat> QueryHabits(
      const HabitQuery& query = {}) const;

 private:
  enum class Phase : std::uint8_t {
    kIdle,
    kDeletingCommittedEntry,
    kAwaitingReplacement,
  };

  struct PendingCommit {
    std::string phrase;
    std::string pinyin;
    std::size_t scalar_count{0};
    std::size_t backspace_count{0};
    bool replacement_started{false};
    TimePoint expires_at;
  };

  [[nodiscard]] bool ExpiredLocked() const;
  void BreakAdjacencyLocked();
  void ArmLocked(const SessionCommit& commit,
                 const LearningUpdate& learning_update);
  void ApplyFeedbackLocked(const LearningUpdate& update);

  std::chrono::milliseconds correction_window_;
  Clock clock_;
  DateBucketProvider date_bucket_provider_;
  mutable std::mutex mutex_;
  CorrectionLearning learning_;
  // The habit aggregate key includes its local date bucket. Keeping this
  // bound separate from correction_scores_ prevents a long-running input
  // method process from accumulating one row per word per day without limit.
  std::unordered_set<std::string> tracked_stat_keys_;
  std::unordered_map<std::string, std::int32_t> correction_scores_;
  Phase phase_{Phase::kIdle};
  std::optional<PendingCommit> pending_;
};

}  // namespace yunpin
