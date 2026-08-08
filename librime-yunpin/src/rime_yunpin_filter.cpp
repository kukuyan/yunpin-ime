// SPDX-License-Identifier: Apache-2.0
#include "rime_yunpin_filter.hpp"

#include <algorithm>
#include <fstream>
#include <set>
#include <string>
#include <utility>
#include <vector>

#include <rime/candidate.h>
#include <rime/context.h>
#include <rime/engine.h>
#include <rime/schema.h>
#include <rime/segmentation.h>
#include <rime/service.h>
#include <rime/translation.h>

namespace rime {
namespace {

constexpr char kSearchActionPrefix[] = "yunpin-search:";
constexpr char kFavoriteActionPrefix[] = "yunpin-fav:";

class YunPinSearchCandidate : public SimpleCandidate {
 public:
  explicit YunPinSearchCandidate(std::size_t start,
                                std::size_t end,
                                const std::string& input)
      : SimpleCandidate("yunpin-search", start, end,
                        kSearchActionPrefix + input,
                        "点击联网搜索：GIF / 图片（梗图）") {}
};

class YunPinFavoriteCandidate : public SimpleCandidate {
 public:
  explicit YunPinFavoriteCandidate(std::size_t start,
                                  std::size_t end,
                                  const std::string& input)
      : SimpleCandidate("yunpin-fav", start, end,
                        kFavoriteActionPrefix + input,
                        "点击收藏到表情收藏夹") {}
};

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

// Ordering contract: `front` candidates (private phrases) take the head of the
// list, then upstream. `deferred` candidates are parked at `deferred_offset`
// so that they never occupy a head slot -- the first candidate is what the
// space bar commits, and 1/2 are the most used selection keys. When upstream
// runs dry before the offset the deferred candidates are still emitted rather
// than silently dropped.
class YunPinMergedTranslation : public Translation {
 public:
  YunPinMergedTranslation(an<Translation> upstream,
                          std::vector<of<Candidate>> front,
                          std::vector<of<Candidate>> deferred,
                          std::size_t deferred_offset)
      : upstream_(std::move(upstream)),
        front_(std::move(front)),
        deferred_(std::move(deferred)),
        deferred_offset_(deferred_offset) {
    for (const auto& candidate : front_) {
      injected_text_.insert(candidate->text());
    }
    for (const auto& candidate : deferred_) {
      injected_text_.insert(candidate->text());
    }
    SkipDuplicateUpstream();
    RefreshExhausted();
  }

  bool Next() override {
    switch (SelectSource()) {
      case Source::kFront:
        ++front_cursor_;
        break;
      case Source::kDeferred:
        ++deferred_cursor_;
        break;
      case Source::kUpstream:
        upstream_->Next();
        SkipDuplicateUpstream();
        break;
      case Source::kNone:
        return false;
    }
    ++emitted_;
    RefreshExhausted();
    return true;
  }

  an<Candidate> Peek() override {
    switch (SelectSource()) {
      case Source::kFront:
        return front_[front_cursor_];
      case Source::kDeferred:
        return deferred_[deferred_cursor_];
      case Source::kUpstream:
        return upstream_->Peek();
      case Source::kNone:
        break;
    }
    return nullptr;
  }

 private:
  enum class Source { kFront, kDeferred, kUpstream, kNone };

  // Single decision point shared by Peek and Next so the two can never
  // disagree about which candidate is current.
  Source SelectSource() const {
    if (front_cursor_ < front_.size()) {
      return Source::kFront;
    }
    const bool upstream_available = upstream_ && !upstream_->exhausted();
    const bool deferred_available = deferred_cursor_ < deferred_.size();
    if (deferred_available &&
        (emitted_ >= deferred_offset_ || !upstream_available)) {
      return Source::kDeferred;
    }
    if (upstream_available) {
      return Source::kUpstream;
    }
    return deferred_available ? Source::kDeferred : Source::kNone;
  }

  void SkipDuplicateUpstream() {
    while (upstream_ && !upstream_->exhausted()) {
      const auto candidate = upstream_->Peek();
      if (!candidate || injected_text_.find(candidate->text()) ==
                            injected_text_.end()) {
        break;
      }
      upstream_->Next();
    }
  }

  void RefreshExhausted() { set_exhausted(SelectSource() == Source::kNone); }

  an<Translation> upstream_;
  std::vector<of<Candidate>> front_;
  std::vector<of<Candidate>> deferred_;
  std::set<std::string> injected_text_;
  std::size_t front_cursor_{0};
  std::size_t deferred_cursor_{0};
  std::size_t deferred_offset_{0};
  std::size_t emitted_{0};
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

YunPinFilter::YunPinFilter(const Ticket& ticket) : Filter(ticket) {
  if (ticket.schema) {
    Config* config = ticket.schema->config();
    config->GetString(name_space_ + "/tag", &tag_);
    config->GetString(name_space_ + "/snapshot", &snapshot_path_);
    config->GetBool(name_space_ + "/enabled", &enabled_);
    config->GetBool(name_space_ + "/expression_search", &expression_search_);
    int configured_limit = static_cast<int>(max_candidates_);
    if (config->GetInt(name_space_ + "/max_candidates", &configured_limit)) {
      max_candidates_ = static_cast<std::size_t>(
          std::clamp(configured_limit, 0, 2));
    }
  }
  // The module master switch still wins: a deployment that turns the filter
  // off must not regain the expression actions through a stale overlay.
  if (!enabled_) {
    expression_search_ = false;
  }
  // Snapshot availability now only gates the private phrases. Previously a
  // missing snapshot disabled the whole filter and a present one silently
  // switched the expression actions on.
  if (enabled_ && max_candidates_ > 0) {
    private_ready_ = LoadSnapshot(snapshot_path_);
  }
}

bool YunPinFilter::Active() const {
  return enabled_ && (private_ready_ || expression_search_);
}

std::size_t YunPinFilter::PageSize() const {
  if (engine_ && engine_->schema()) {
    const int configured = engine_->schema()->page_size();
    if (configured > 0) {
      return static_cast<std::size_t>(configured);
    }
  }
  return 5;
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
    LOG(INFO) << "yunpin private snapshot is not present; filter disabled";
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
  if (!context) {
    return true;
  }
  return context->get_option("yunpin_private_mode") ||
         context->get_option("password_mode") ||
         context->get_option("incognito_mode");
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
  std::string trimmed_input = active_input_;
  while (!trimmed_input.empty() &&
         (trimmed_input.front() == ' ' || trimmed_input.front() == '\t')) {
    trimmed_input.erase(trimmed_input.begin());
  }
  while (!trimmed_input.empty() &&
         (trimmed_input.back() == ' ' || trimmed_input.back() == '\t')) {
    trimmed_input.pop_back();
  }
  // Private phrases keep the head slots; max_candidates_ already bounds them.
  std::vector<of<Candidate>> front;
  if (private_ready_) {
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

  std::vector<of<Candidate>> deferred;
  if (expression_search_ && !trimmed_input.empty()) {
    deferred.reserve(2);
    deferred.push_back(
        New<YunPinSearchCandidate>(active_start_, active_end_, trimmed_input));
    deferred.push_back(
        New<YunPinFavoriteCandidate>(active_start_, active_end_, trimmed_input));
  }

  if (front.empty() && deferred.empty()) {
    return translation;
  }

  // Park the actions in the trailing slots of the first page: visible without
  // a page turn, but off the space bar and off keys 1-2.
  std::size_t deferred_offset = front.size();
  const std::size_t page_size = PageSize();
  if (page_size > deferred.size()) {
    deferred_offset = std::max(deferred_offset, page_size - deferred.size());
  }

  return New<YunPinMergedTranslation>(std::move(translation), std::move(front),
                                      std::move(deferred), deferred_offset);
}

}  // namespace rime
