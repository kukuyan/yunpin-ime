// SPDX-License-Identifier: Apache-2.0
#include "rime_yunpin_filter.hpp"

#include <algorithm>
#include <cstdint>
#include <fstream>
#include <functional>
#include <set>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

#include <rime/candidate.h>
#include <rime/context.h>
#include <rime/engine.h>
#include <rime/key_event.h>
#include <rime/schema.h>
#include <rime/segmentation.h>
#include <rime/service.h>
#include <rime/translation.h>

#include "yunpin/native_selection_events.hpp"

namespace rime {
namespace {

bool IsCjkIdeograph(std::uint32_t codepoint) noexcept {
  return (codepoint >= 0x3400 && codepoint <= 0x4dbf) ||
         (codepoint >= 0x4e00 && codepoint <= 0x9fff) ||
         (codepoint >= 0xf900 && codepoint <= 0xfaff) ||
         (codepoint >= 0x20000 && codepoint <= 0x2fa1f) ||
         (codepoint >= 0x30000 && codepoint <= 0x323af);
}

// Returns true only after validating the complete UTF-8 string. Malformed,
// overlong, surrogate and out-of-range encodings are kept as ordinary upstream
// data rather than guessed at or suppressed.
bool IsPureCjkAtLeast(std::string_view text,
                      std::size_t minimum_scalars) noexcept {
  std::size_t scalars = 0;
  bool pure_cjk = true;
  for (std::size_t offset = 0; offset < text.size();) {
    const unsigned char first = static_cast<unsigned char>(text[offset]);
    std::uint32_t codepoint = 0;
    std::size_t width = 0;
    std::uint32_t minimum = 0;
    if (first < 0x80) {
      codepoint = first;
      width = 1;
    } else if ((first & 0xe0) == 0xc0) {
      codepoint = first & 0x1f;
      width = 2;
      minimum = 0x80;
    } else if ((first & 0xf0) == 0xe0) {
      codepoint = first & 0x0f;
      width = 3;
      minimum = 0x800;
    } else if ((first & 0xf8) == 0xf0) {
      codepoint = first & 0x07;
      width = 4;
      minimum = 0x10000;
    } else {
      return false;
    }
    if (offset + width > text.size()) {
      return false;
    }
    for (std::size_t index = 1; index < width; ++index) {
      const unsigned char continuation =
          static_cast<unsigned char>(text[offset + index]);
      if ((continuation & 0xc0) != 0x80) {
        return false;
      }
      codepoint = (codepoint << 6) | (continuation & 0x3f);
    }
    if ((width > 1 && codepoint < minimum) || codepoint > 0x10ffff ||
        (codepoint >= 0xd800 && codepoint <= 0xdfff)) {
      return false;
    }
    pure_cjk = pure_cjk && IsCjkIdeograph(codepoint);
    ++scalars;
    offset += width;
  }
  return pure_cjk && scalars >= minimum_scalars;
}

bool IsShortNormalizedPinyin(std::string_view input) {
  const std::string normalized = yunpin::NormalizePinyin(input);
  return !normalized.empty() && normalized.size() <= 2;
}

yunpin::LearningContext LearningContextFor(const Context* context) {
  if (!context) {
    return yunpin::LearningContext::kPrivate;
  }
  // Learning is an explicit host capability, never an inference from the
  // absence of a password/private flag.  Until Squirrel/Weasel bridge a
  // trustworthy secure-field signal, this option remains false and both
  // learning and event publication fail closed.
  if (!context->get_option("yunpin_learning_allowed")) {
    return yunpin::LearningContext::kPrivate;
  }
  if (context->get_option("password_mode")) {
    return yunpin::LearningContext::kPassword;
  }
  if (context->get_option("yunpin_one_shot") ||
      context->get_option("one_shot_mode") ||
      context->get_option("one_time_input")) {
    return yunpin::LearningContext::kOneShot;
  }
  if (context->get_option("yunpin_private_mode") ||
      context->get_option("incognito_mode")) {
    return yunpin::LearningContext::kPrivate;
  }
  return yunpin::LearningContext::kNormal;
}

bool IsWordCandidateType(std::string_view type) noexcept {
  return type == "phrase" || type == "user_phrase" || type == "table" ||
         type == "user_table" || type == "completion";
}

class YunPinCandidate : public SimpleCandidate {
 public:
  YunPinCandidate(std::string id,
                  std::size_t start,
                  std::size_t end,
                  const yunpin::Candidate& candidate)
      : SimpleCandidate("yunpin", start, end, candidate.text,
                        candidate.pinned ? "\xE2\x98\x85" : ""),
        id_(std::move(id)) {}

  const std::string& id() const noexcept { return id_; }

 private:
  std::string id_;
};

// Ordering contract: bounded private phrases take the head of the list, then
// the ordinary upstream translation. Expression actions are deliberately not
// injected until a frontend can carry a typed, explicitly armed action without
// encoding it as ordinary commit text.
class YunPinMergedTranslation : public Translation {
 public:
  YunPinMergedTranslation(an<Translation> upstream,
                          std::vector<of<Candidate>> front,
                          bool suppress_long_cjk_upstream,
                          bool protect_long_corrections,
                          std::function<std::int32_t(std::string_view)>
                              correction_score)
      : upstream_(std::move(upstream)),
        front_(std::move(front)),
        suppress_long_cjk_upstream_(suppress_long_cjk_upstream),
        protect_long_corrections_(protect_long_corrections) {
    for (const auto& candidate : front_) {
      injected_text_.insert(candidate->text());
    }
    PrepareUpstreamWindow(correction_score);
    RefreshExhausted();
  }

  bool Next() override {
    switch (SelectSource()) {
      case Source::kFront:
        ++front_cursor_;
        break;
      case Source::kWindow:
        ++window_cursor_;
        if (window_cursor_ == upstream_window_.size()) {
          SkipSuppressedUpstream();
        }
        break;
      case Source::kUpstream:
        upstream_->Next();
        SkipSuppressedUpstream();
        break;
      case Source::kNone:
        return false;
    }
    RefreshExhausted();
    return true;
  }

  an<Candidate> Peek() override {
    switch (SelectSource()) {
      case Source::kFront:
        return front_[front_cursor_];
      case Source::kWindow:
        return upstream_window_[window_cursor_];
      case Source::kUpstream:
        return upstream_->Peek();
      case Source::kNone:
        break;
    }
    return nullptr;
  }

 private:
  enum class Source { kFront, kWindow, kUpstream, kNone };

  // Single decision point shared by Peek and Next so the two can never
  // disagree about which candidate is current.
  Source SelectSource() const {
    if (front_cursor_ < front_.size()) {
      return Source::kFront;
    }
    if (window_cursor_ < upstream_window_.size()) {
      return Source::kWindow;
    }
    if (upstream_ && !upstream_->exhausted()) {
      return Source::kUpstream;
    }
    return Source::kNone;
  }

  bool Suppressed(const an<Candidate>& candidate,
                  bool discard_long_correction) const {
    if (!candidate) {
      return false;
    }
    const bool duplicate = injected_text_.find(candidate->text()) !=
                           injected_text_.end();
    const bool overlong_short_input_prediction =
        suppress_long_cjk_upstream_ &&
        IsPureCjkAtLeast(candidate->text(), 3);
    const bool late_correction = discard_long_correction &&
                                 protect_long_corrections_ &&
                                 candidate->is_correction();
    return duplicate || overlong_short_input_prediction || late_correction;
  }

  void SkipSuppressedUpstream() {
    while (upstream_ && !upstream_->exhausted()) {
      const auto candidate = upstream_->Peek();
      if (!candidate || !Suppressed(candidate, true)) {
        break;
      }
      upstream_->Next();
    }
  }

  void PrepareUpstreamWindow(
      const std::function<std::int32_t(std::string_view)>& correction_score) {
    constexpr std::size_t kCandidatePageSize = 8;
    const std::size_t window_limit =
        front_.size() < kCandidatePageSize
            ? kCandidatePageSize - front_.size()
            : 0;
    while (upstream_ && !upstream_->exhausted() &&
           upstream_window_.size() < window_limit) {
      const auto candidate = upstream_->Peek();
      if (!candidate) {
        break;
      }
      // Corrections inside the bounded first page are retained temporarily so
      // ProtectLongCorrections can choose at most one. Once this window has
      // been consumed, every later correction is dropped instead of leaking
      // onto another page.
      if (!Suppressed(candidate, false)) {
        upstream_window_.push_back(candidate);
      }
      upstream_->Next();
    }
    std::stable_sort(
        upstream_window_.begin(), upstream_window_.end(),
        [&](const of<Candidate>& left, const of<Candidate>& right) {
          return correction_score(left->text()) > correction_score(right->text());
        });
    ProtectLongCorrections();
    SkipSuppressedUpstream();
  }

  void ProtectLongCorrections() {
    if (!protect_long_corrections_ || upstream_window_.empty()) {
      return;
    }

    const auto first_ordinary = std::find_if(
        upstream_window_.begin(), upstream_window_.end(),
        [](const of<Candidate>& candidate) {
          return candidate && !candidate->is_correction();
        });
    if (first_ordinary == upstream_window_.end()) {
      // With no ordinary evidence in the bounded page, fail closed. A later
      // ordinary upstream candidate can still stream through, but an
      // automatic correction never becomes the only visible choice.
      upstream_window_.erase(
          std::remove_if(upstream_window_.begin(), upstream_window_.end(),
                         [](const of<Candidate>& candidate) {
                           return candidate && candidate->is_correction();
                         }),
          upstream_window_.end());
      return;
    }

    std::vector<of<Candidate>> ordered;
    ordered.reserve(upstream_window_.size());
    of<Candidate> selected_correction;
    for (const auto& candidate : upstream_window_) {
      if (candidate && candidate->is_correction()) {
        if (!selected_correction) {
          selected_correction = candidate;
        }
      } else if (candidate) {
        ordered.push_back(candidate);
      }
    }

    // One ordinary candidate must always precede recovery. With no private
    // head the correction can be total rank #2; with one private head it can
    // be total rank #3. Two private candidates leave no safe #2/#3 slot, so
    // correction stays hidden. Every other correction is discarded rather
    // than moved to a later page.
    if (selected_correction && front_.size() < 2 && !ordered.empty()) {
      ordered.insert(ordered.begin() + 1, selected_correction);
    }
    upstream_window_ = std::move(ordered);
  }

  void RefreshExhausted() { set_exhausted(SelectSource() == Source::kNone); }

  an<Translation> upstream_;
  std::vector<of<Candidate>> front_;
  std::vector<of<Candidate>> upstream_window_;
  std::set<std::string> injected_text_;
  std::size_t front_cursor_{0};
  std::size_t window_cursor_{0};
  bool suppress_long_cjk_upstream_{false};
  bool protect_long_corrections_{false};
};

bool IsSafeRelativePath(const std::string& path) {
  if (path.empty() || path.front() == '/' || path.front() == '\\' ||
      path.find(':') != std::string::npos) {
    return false;
  }
  std::size_t start = 0;
  while (start <= path.size()) {
    const std::size_t slash = path.find_first_of("/\\", start);
    const std::string part =
        path.substr(start, slash == std::string::npos ? std::string::npos
                                                      : slash - start);
    if (part.empty() || part == "." || part == "..") {
      return false;
    }
    if (slash == std::string::npos) {
      break;
    }
    start = slash + 1;
  }
  return true;
}

}  // namespace

// Session-owned callback target.  Notifier slots carry only weak references,
// so neither a stale signal nor a filtered translation can call through a
// destroyed YunPinFilter.  The state itself stays alive for an already-running
// callback and is released immediately afterwards.
class YunPinSessionLearningBridge {
 public:
  YunPinSessionLearningBridge() = default;

  void OnCommit(Context* context) {
    if (!context ||
        LearningContextFor(context) != yunpin::LearningContext::kNormal) {
      learning_.BreakAdjacency();
      return;
    }

    const auto& composition = context->composition();
    const std::string& input = context->input();
    if (composition.empty() || composition.size() > 2) {
      learning_.BreakAdjacency();
      return;
    }
    const Segment& segment = composition.front();
    if (composition.size() == 2) {
      const Segment& placeholder = composition.back();
      if (placeholder.start != input.size() ||
          placeholder.end != input.size() ||
          placeholder.GetSelectedCandidate()) {
        learning_.BreakAdjacency();
        return;
      }
    }
    const auto candidate = segment.GetSelectedCandidate();
    const auto genuine = candidate ? Candidate::GetGenuineCandidate(candidate)
                                   : an<Candidate>();
    if (!candidate || segment.status < Segment::kSelected ||
        segment.start != 0 || segment.end != input.size() ||
        candidate->start() != 0 || candidate->end() != input.size() ||
        !genuine || !IsWordCandidateType(genuine->type()) ||
        context->GetCommitText() != candidate->text()) {
      learning_.BreakAdjacency();
      return;
    }

    const std::string normalized = yunpin::NormalizePinyin(input);
    if (learning_.ObserveCommit(yunpin::SessionCommit{
            candidate->text(), normalized,
            yunpin::LearningContext::kNormal})) {
      // Best effort only.  A full/busy queue never delays or rolls back local
      // in-process learning.
      try {
        (void)yunpin::NativeSelectionEventQueue::Instance().TryPublish(
            candidate->text(), normalized);
      } catch (...) {
        // Constructing the process-local queue may allocate on its first use.
        // A resource failure must drop the sync event, never terminate the IME.
      }
    }
  }

  void OnContextUpdate(Context* context) {
    learning_.ObserveComposition(
        context ? std::string_view(context->input()) : std::string_view(),
        LearningContextFor(context));
  }

  void OnUnhandledKey(Context* context, const KeyEvent& key_event) {
    const bool unmodified_backspace =
        key_event.modifier() == 0 && key_event.keycode() == XK_BackSpace;
    learning_.ObserveUnhandledKey(unmodified_backspace,
                                  LearningContextFor(context));
  }

  void BreakAdjacency() { learning_.BreakAdjacency(); }

  [[nodiscard]] std::int32_t CorrectionScore(
      std::string_view pinyin, std::string_view phrase) const {
    return learning_.CorrectionScore(pinyin, phrase);
  }

  [[nodiscard]] std::vector<yunpin::HabitStat> QueryHabits(
      const yunpin::HabitQuery& query) const {
    return learning_.QueryHabits(query);
  }

 private:
  yunpin::SessionLearning learning_;
};

YunPinFilter::YunPinFilter(const Ticket& ticket) : Filter(ticket) {
  if (ticket.schema) {
    Config* config = ticket.schema->config();
    config->GetString(name_space_ + "/tag", &tag_);
    config->GetString(name_space_ + "/snapshot", &snapshot_path_);
    config->GetBool(name_space_ + "/enabled", &enabled_);
    config->GetBool(name_space_ + "/short_input_guard", &short_input_guard_);
    config->GetBool(name_space_ + "/long_correction_guard",
                    &long_correction_guard_);
    config->GetBool(name_space_ + "/session_learning",
                    &session_learning_enabled_);
    int configured_limit = static_cast<int>(max_candidates_);
    if (config->GetInt(name_space_ + "/max_candidates", &configured_limit)) {
      max_candidates_ = static_cast<std::size_t>(
          std::clamp(configured_limit, 0, 2));
    }
    int configured_min_chars =
        static_cast<int>(long_correction_min_chars_);
    if (config->GetInt(name_space_ + "/long_correction_min_chars",
                       &configured_min_chars)) {
      long_correction_min_chars_ = static_cast<std::size_t>(
          std::clamp(configured_min_chars, 6, 64));
    }
  }
  if (enabled_ && max_candidates_ > 0) {
    private_ready_ = LoadSnapshot(snapshot_path_);
  }
  if (session_learning_enabled_ && engine_ && engine_->context()) {
    session_learning_ = std::make_shared<YunPinSessionLearningBridge>();
    const std::weak_ptr<YunPinSessionLearningBridge> weak_learning =
        session_learning_;
    Context* context = engine_->context();
    commit_connection_ = context->commit_notifier().connect(
        [weak_learning](Context* ctx) {
          if (const auto learning = weak_learning.lock()) {
            learning->OnCommit(ctx);
          }
        });
    update_connection_ = context->update_notifier().connect(
        [weak_learning](Context* ctx) {
          if (const auto learning = weak_learning.lock()) {
            learning->OnContextUpdate(ctx);
          }
        });
    unhandled_key_connection_ = context->unhandled_key_notifier().connect(
        [weak_learning](Context* ctx, const KeyEvent& key) {
          if (const auto learning = weak_learning.lock()) {
            learning->OnUnhandledKey(ctx, key);
          }
        });
    option_update_connection_ = context->option_update_notifier().connect(
        [weak_learning](Context*, const string&) {
          if (const auto learning = weak_learning.lock()) {
            learning->BreakAdjacency();
          }
        });
    delete_connection_ = context->delete_notifier().connect(
        [weak_learning](Context*) {
          if (const auto learning = weak_learning.lock()) {
            learning->BreakAdjacency();
          }
        });
  }
}

YunPinFilter::~YunPinFilter() {
  DisconnectLearningNotifiers();
  session_learning_.reset();
}

void YunPinFilter::DisconnectLearningNotifiers() noexcept {
  commit_connection_.disconnect();
  update_connection_.disconnect();
  unhandled_key_connection_.disconnect();
  option_update_connection_.disconnect();
  delete_connection_.disconnect();
}

bool YunPinFilter::Active() const {
  return enabled_ || short_input_guard_ || long_correction_guard_ ||
         session_learning_enabled_;
}

bool YunPinFilter::LoadSnapshot(const std::string& relative_path) {
  if (!IsSafeRelativePath(relative_path)) {
    LOG(ERROR) << "yunpin snapshot path must be a safe relative path";
    return false;
  }
  const path snapshot =
      Service::instance().deployer().user_data_dir / relative_path;
  std::ifstream input(snapshot.string(), std::ios::in | std::ios::binary);
  if (!input) {
    LOG(INFO) << "yunpin private snapshot is not present; private injection "
                 "disabled";
    return false;
  }
  auto result = yunpin::ParsePrivateSnapshot(input);
  if (!result.header_valid) {
    LOG(ERROR) << "yunpin private snapshot has an invalid header";
    return false;
  }
  store_.Replace(std::move(result.entries));
  LOG(INFO) << "yunpin private snapshot loaded: " << result.accepted_rows
            << " accepted, " << result.rejected_rows << " rejected";
  return store_.size() > 0;
}

bool YunPinFilter::PrivateModeEnabled() const {
  const Context* context = engine_ ? engine_->context() : nullptr;
  return LearningContextFor(context) != yunpin::LearningContext::kNormal;
}

std::vector<yunpin::HabitStat> YunPinFilter::QueryHabits(
    const yunpin::HabitQuery& query) const {
  return session_learning_ ? session_learning_->QueryHabits(query)
                           : std::vector<yunpin::HabitStat>();
}

bool YunPinFilter::AppliesToSegment(Segment* segment) {
  active_input_.clear();
  if (!Active() || segment == nullptr || !segment->HasTag(tag_) ||
      PrivateModeEnabled() || !engine_ || !engine_->context()) {
    return false;
  }
  const std::string& input = engine_->context()->input();
  if (segment->end > input.size() || segment->start >= segment->end) {
    return false;
  }
  active_start_ = segment->start;
  active_end_ = segment->end;
  active_input_ = input.substr(active_start_, active_end_ - active_start_);
  return !active_input_.empty();
}

an<Translation> YunPinFilter::Apply(an<Translation> translation,
                                    CandidateList* candidates) {
  (void)candidates;
  if (active_input_.empty()) {
    return translation;
  }
  // Private phrases keep the head slots; max_candidates_ already bounds them.
  const bool suppress_long_cjk_upstream =
      short_input_guard_ && IsShortNormalizedPinyin(active_input_);
  const std::string normalized = yunpin::NormalizePinyin(active_input_);
  const bool protect_long_corrections =
      long_correction_guard_ &&
      normalized.size() >= long_correction_min_chars_;
  std::vector<of<Candidate>> front;
  if (enabled_ && private_ready_) {
    const auto matches = store_.Query(active_input_, max_candidates_);
    front.reserve(matches.size());
    std::set<std::string> seen;
    for (const auto& match : matches) {
      if (!seen.insert(match.text).second) {
        continue;
      }
      front.push_back(New<YunPinCandidate>(match.id, active_start_,
                                           active_end_, match));
    }
  }

  if (front.empty() && !suppress_long_cjk_upstream &&
      !protect_long_corrections) {
    // A learned correction can still reorder the bounded upstream window.
    if (!session_learning_ || normalized.empty()) {
      return translation;
    }
  }
  const auto correction_score =
      [weak_learning =
           std::weak_ptr<YunPinSessionLearningBridge>(session_learning_),
       normalized](std::string_view text) {
        const auto learning = weak_learning.lock();
        return learning && !normalized.empty()
                   ? learning->CorrectionScore(normalized, text)
                   : std::int32_t{0};
      };
  return New<YunPinMergedTranslation>(std::move(translation), std::move(front),
                                      suppress_long_cjk_upstream,
                                      protect_long_corrections,
                                      correction_score);
}

}  // namespace rime
