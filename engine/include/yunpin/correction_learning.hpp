// SPDX-License-Identifier: Apache-2.0
#pragma once

#include <cstddef>
#include <cstdint>
#include <iosfwd>
#include <mutex>
#include <optional>
#include <string>
#include <unordered_map>
#include <vector>

namespace yunpin {

// Hosts must set the real input context. Only kNormal events are eligible for
// learning or monitoring; the other contexts also break any pending
// correction chain so a later normal selection cannot be associated with
// private input.
enum class LearningContext : std::uint8_t {
  kNormal,
  kPrivate,
  kPassword,
  kOneShot,
};

// Deliberately entry-scoped: there is no surrounding sentence, application
// name, window title, or raw composition field in this model.
struct SelectionEvent {
  std::string date_bucket;  // YYYY-MM-DD, supplied by the desktop host.
  std::string entry_id;
  std::string phrase;
  std::string pinyin;
  LearningContext context{LearningContext::kNormal};
  bool monitorable{true};
};

struct FeedbackDelta {
  std::string entry_id;
  std::int32_t delta{0};
};

struct LearningUpdate {
  bool recorded{false};
  bool correction_completed{false};
  bool requires_requery{false};
  std::uint64_t candidate_epoch{0};
  std::vector<FeedbackDelta> feedback;
};

struct HabitStat {
  std::string date_bucket;
  std::string entry_id;
  std::string phrase;
  std::string pinyin;
  std::uint64_t selection_count{0};
  std::uint64_t corrected_from_count{0};
  std::uint64_t replacement_count{0};

  [[nodiscard]] std::int64_t net_correction_feedback() const noexcept;
};

struct HabitQuery {
  std::string date_bucket;  // Empty means all local date buckets.
  bool corrections_only{false};
  std::size_t limit{100};
};

// A small, network-free state machine for the narrow sequence:
//   select old -> undo/delete the just committed entry -> select replacement.
// The host must call BreakAdjacency() when any unrelated edit occurs. The hot
// path only updates in-memory word-level aggregates; persistence is an explicit
// background snapshot operation through WriteHabitStatsTsv().
class CorrectionLearning {
 public:
  [[nodiscard]] LearningUpdate RecordSelection(SelectionEvent event);
  [[nodiscard]] bool UndoLastSelection(
      LearningContext context = LearningContext::kNormal);
  void BreakAdjacency();

  [[nodiscard]] std::vector<HabitStat> Query(
      const HabitQuery& query = {}) const;
  [[nodiscard]] std::uint64_t candidate_epoch() const;
  [[nodiscard]] bool CanReuseCandidateEpoch(
      std::uint64_t cached_epoch) const;

 private:
  [[nodiscard]] static std::string StatKey(const SelectionEvent& event);
  void ClearAdjacencyLocked();

  mutable std::mutex mutex_;
  std::vector<HabitStat> stats_;
  std::unordered_map<std::string, std::size_t> stat_indexes_;
  std::optional<SelectionEvent> last_selection_;
  std::optional<SelectionEvent> pending_wrong_;
  std::uint64_t candidate_epoch_{0};
};

// Conservative validation is applied both while recording and while reading a
// monitor snapshot. Obvious URLs, email/path-like values, secret labels, long
// digit runs, control characters, and oversized entries are rejected without
// echoing them.
[[nodiscard]] bool IsMonitorableSelection(const SelectionEvent& event);

// Explicit, user-requested plaintext report interchange for the local CLI.
// This is NOT a persistence format: background state must remain in the
// encrypted localstore and feed Query()/the report renderer only on demand.
// Neither function opens a file or socket; callers should use a pipe and must
// never retain the plaintext report automatically.
void ExportHabitReportTsv(std::ostream& output,
                          const std::vector<HabitStat>& stats);
[[nodiscard]] std::vector<HabitStat> ParseHabitReportTsv(
    std::istream& input, std::size_t max_rows = 50000);

[[nodiscard]] std::vector<HabitStat> BuildHabitReport(
    const std::vector<HabitStat>& stats, const HabitQuery& query = {});

}  // namespace yunpin
