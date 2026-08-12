// SPDX-License-Identifier: Apache-2.0
#include "yunpin/session_learning.hpp"

#include "yunpin/phrase_engine.hpp"

#include <algorithm>
#include <array>
#include <ctime>
#include <iomanip>
#include <limits>
#include <sstream>
#include <utility>

namespace yunpin {
namespace {

constexpr std::int32_t kMaxSessionCorrectionScore = 1000000;

std::optional<std::size_t> Utf8ScalarCount(std::string_view text) noexcept {
  std::size_t scalars = 0;
  for (std::size_t offset = 0; offset < text.size();) {
    const unsigned char first = static_cast<unsigned char>(text[offset]);
    std::uint32_t codepoint = 0;
    std::uint32_t minimum = 0;
    std::size_t width = 0;
    if (first < 0x80) {
      codepoint = first;
      width = 1;
    } else if ((first & 0xe0) == 0xc0) {
      codepoint = first & 0x1f;
      minimum = 0x80;
      width = 2;
    } else if ((first & 0xf0) == 0xe0) {
      codepoint = first & 0x0f;
      minimum = 0x800;
      width = 3;
    } else if ((first & 0xf8) == 0xf0) {
      codepoint = first & 0x07;
      minimum = 0x10000;
      width = 4;
    } else {
      return std::nullopt;
    }
    if (offset + width > text.size()) {
      return std::nullopt;
    }
    for (std::size_t index = 1; index < width; ++index) {
      const unsigned char continuation =
          static_cast<unsigned char>(text[offset + index]);
      if ((continuation & 0xc0) != 0x80) {
        return std::nullopt;
      }
      codepoint = (codepoint << 6) | (continuation & 0x3f);
    }
    if ((width > 1 && codepoint < minimum) || codepoint > 0x10ffff ||
        (codepoint >= 0xd800 && codepoint <= 0xdfff)) {
      return std::nullopt;
    }
    ++scalars;
    offset += width;
  }
  return scalars == 0 ? std::nullopt
                      : std::optional<std::size_t>(scalars);
}

std::uint64_t Fnv1a(std::string_view value, std::uint64_t seed) noexcept {
  std::uint64_t hash = seed;
  for (const unsigned char byte : value) {
    hash ^= byte;
    hash *= 1099511628211ULL;
  }
  return hash;
}

std::string EntryId(std::string_view pinyin, std::string_view phrase) {
  const std::uint64_t left =
      Fnv1a(pinyin, 1469598103934665603ULL) ^
      Fnv1a(phrase, 1099511628211ULL);
  const std::uint64_t right =
      Fnv1a(phrase, 7809847782465536322ULL) ^
      Fnv1a(pinyin, 1609587929392839161ULL);
  std::ostringstream output;
  output << "rime" << std::hex << std::setfill('0');
  for (const std::uint64_t half : {left, right}) {
    for (int shift = 48; shift >= 0; shift -= 16) {
      output << '-' << std::setw(4) << ((half >> shift) & 0xffffULL);
    }
  }
  return output.str();
}

std::string DefaultDateBucket() {
  const std::time_t now = std::time(nullptr);
  std::tm local{};
#if defined(_WIN32)
  if (localtime_s(&local, &now) != 0) {
    return {};
  }
#else
  if (localtime_r(&now, &local) == nullptr) {
    return {};
  }
#endif
  std::array<char, 11> date{};
  if (std::strftime(date.data(), date.size(), "%Y-%m-%d", &local) != 10) {
    return {};
  }
  return date.data();
}

bool StartsWith(std::string_view value, std::string_view prefix) noexcept {
  return value.size() >= prefix.size() &&
         value.compare(0, prefix.size(), prefix) == 0;
}

}  // namespace

SessionLearning::SessionLearning(
    std::chrono::milliseconds correction_window, Clock clock,
    DateBucketProvider date_bucket_provider)
    : correction_window_(std::max(correction_window,
                                  std::chrono::milliseconds(1))),
      clock_(clock ? std::move(clock)
                   : Clock([] { return std::chrono::steady_clock::now(); })),
      date_bucket_provider_(date_bucket_provider
                                ? std::move(date_bucket_provider)
                                : DateBucketProvider(DefaultDateBucket)) {}

bool SessionLearning::ExpiredLocked() const {
  return pending_.has_value() && clock_() > pending_->expires_at;
}

void SessionLearning::BreakAdjacencyLocked() {
  phase_ = Phase::kIdle;
  pending_.reset();
  learning_.BreakAdjacency();
}

void SessionLearning::ArmLocked(const SessionCommit& commit,
                                const LearningUpdate& learning_update) {
  const auto scalars = Utf8ScalarCount(commit.phrase);
  if (!learning_update.recorded || !scalars.has_value()) {
    phase_ = Phase::kIdle;
    pending_.reset();
    return;
  }
  pending_ = PendingCommit{commit.phrase, NormalizePinyin(commit.pinyin),
                           *scalars, 0, false,
                           clock_() + correction_window_};
  phase_ = Phase::kDeletingCommittedEntry;
}

void SessionLearning::ApplyFeedbackLocked(const LearningUpdate& update) {
  for (const FeedbackDelta& feedback : update.feedback) {
    const auto found = correction_scores_.find(feedback.entry_id);
    if (found == correction_scores_.end() &&
        correction_scores_.size() >= kMaxTrackedEntries) {
      // Existing scores remain updatable at the limit, but a new key must not
      // make the session grow without bound.
      continue;
    }
    std::int32_t& score = found == correction_scores_.end()
                              ? correction_scores_.emplace(feedback.entry_id, 0)
                                    .first->second
                              : found->second;
    if (feedback.delta > 0) {
      score = score > kMaxSessionCorrectionScore - feedback.delta
                  ? kMaxSessionCorrectionScore
                  : static_cast<std::int32_t>(score + feedback.delta);
    } else if (feedback.delta < 0) {
      score = score < -kMaxSessionCorrectionScore - feedback.delta
                  ? -kMaxSessionCorrectionScore
                  : static_cast<std::int32_t>(score + feedback.delta);
    }
  }
}

bool SessionLearning::ObserveCommit(SessionCommit commit) {
  std::lock_guard<std::mutex> lock(mutex_);
  const std::string normalized = NormalizePinyin(commit.pinyin);
  if (commit.context != LearningContext::kNormal || normalized.empty() ||
      commit.phrase.empty()) {
    BreakAdjacencyLocked();
    return false;
  }
  commit.pinyin = normalized;

  const bool valid_replacement =
      phase_ == Phase::kAwaitingReplacement && pending_.has_value() &&
      !ExpiredLocked() && pending_->pinyin == normalized &&
      pending_->phrase != commit.phrase;
  if (!valid_replacement && phase_ != Phase::kIdle) {
    BreakAdjacencyLocked();
  }

  const std::string entry_id = EntryId(normalized, commit.phrase);
  const std::string date_bucket = date_bucket_provider_();
  const std::string stat_key = date_bucket + '\x1f' + entry_id;
  const bool known_stat = tracked_stat_keys_.find(stat_key) !=
                          tracked_stat_keys_.end();
  if (!known_stat && tracked_stat_keys_.size() >= kMaxTrackedEntries) {
    BreakAdjacencyLocked();
    return false;
  }
  SelectionEvent event{date_bucket, entry_id, commit.phrase, normalized};
  const LearningUpdate update = learning_.RecordSelection(std::move(event));
  if (update.recorded && !known_stat) {
    tracked_stat_keys_.insert(stat_key);
  }
  if (valid_replacement && update.correction_completed &&
      update.requires_requery) {
    ApplyFeedbackLocked(update);
  }
  ArmLocked(commit, update);
  return update.recorded;
}

void SessionLearning::ObserveUnhandledKey(bool is_unmodified_backspace,
                                          LearningContext context) {
  std::lock_guard<std::mutex> lock(mutex_);
  if (phase_ == Phase::kIdle) {
    return;
  }
  if (context != LearningContext::kNormal || !is_unmodified_backspace ||
      ExpiredLocked() || !pending_.has_value() ||
      phase_ != Phase::kDeletingCommittedEntry) {
    BreakAdjacencyLocked();
    return;
  }

  ++pending_->backspace_count;
  if (pending_->backspace_count > pending_->scalar_count) {
    BreakAdjacencyLocked();
    return;
  }
  if (pending_->backspace_count == pending_->scalar_count) {
    if (!learning_.UndoLastSelection()) {
      BreakAdjacencyLocked();
      return;
    }
    phase_ = Phase::kAwaitingReplacement;
  }
}

void SessionLearning::ObserveComposition(std::string_view input,
                                         LearningContext context) {
  std::lock_guard<std::mutex> lock(mutex_);
  if (phase_ == Phase::kIdle) {
    return;
  }
  if (context != LearningContext::kNormal || ExpiredLocked() ||
      !pending_.has_value()) {
    BreakAdjacencyLocked();
    return;
  }

  const std::string normalized = NormalizePinyin(input);
  if (input.empty()) {
    if (phase_ == Phase::kAwaitingReplacement &&
        pending_->replacement_started) {
      BreakAdjacencyLocked();
    }
    return;
  }
  if (normalized.empty() || phase_ != Phase::kAwaitingReplacement ||
      !StartsWith(pending_->pinyin, normalized)) {
    BreakAdjacencyLocked();
    return;
  }
  pending_->replacement_started = true;
}

void SessionLearning::BreakAdjacency() {
  std::lock_guard<std::mutex> lock(mutex_);
  BreakAdjacencyLocked();
}

std::int32_t SessionLearning::CorrectionScore(
    std::string_view pinyin, std::string_view phrase) const {
  const std::string normalized = NormalizePinyin(pinyin);
  if (normalized.empty() || phrase.empty()) {
    return 0;
  }
  const std::string id = EntryId(normalized, phrase);
  std::lock_guard<std::mutex> lock(mutex_);
  const auto found = correction_scores_.find(id);
  return found == correction_scores_.end() ? 0 : found->second;
}

std::vector<HabitStat> SessionLearning::QueryHabits(
    const HabitQuery& query) const {
  return learning_.Query(query);
}

}  // namespace yunpin
