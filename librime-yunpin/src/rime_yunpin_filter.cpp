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

class YunPinMergedTranslation : public Translation {
 public:
  YunPinMergedTranslation(an<Translation> upstream,
                          std::vector<of<Candidate>> injected)
      : upstream_(std::move(upstream)), injected_(std::move(injected)) {
    for (const auto& candidate : injected_) {
      injected_text_.insert(candidate->text());
    }
    SkipDuplicateUpstream();
    RefreshExhausted();
  }

  bool Next() override {
    if (exhausted()) {
      return false;
    }
    if (injected_cursor_ < injected_.size()) {
      ++injected_cursor_;
    } else if (upstream_ && !upstream_->exhausted()) {
      upstream_->Next();
      SkipDuplicateUpstream();
    }
    RefreshExhausted();
    return true;
  }

  an<Candidate> Peek() override {
    if (injected_cursor_ < injected_.size()) {
      return injected_[injected_cursor_];
    }
    if (!upstream_ || upstream_->exhausted()) {
      return nullptr;
    }
    return upstream_->Peek();
  }

 private:
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

  void RefreshExhausted() {
    set_exhausted(injected_cursor_ >= injected_.size() &&
                  (!upstream_ || upstream_->exhausted()));
  }

  an<Translation> upstream_;
  std::vector<of<Candidate>> injected_;
  std::set<std::string> injected_text_;
  std::size_t injected_cursor_{0};
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
    int configured_limit = static_cast<int>(max_candidates_);
    if (config->GetInt(name_space_ + "/max_candidates", &configured_limit)) {
      max_candidates_ = static_cast<std::size_t>(
          std::clamp(configured_limit, 0, 2));
    }
  }
  if (enabled_ && max_candidates_ > 0) {
    enabled_ = LoadSnapshot(snapshot_path_);
  }
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
  if (!enabled_ || max_candidates_ == 0 || segment == nullptr ||
      !segment->HasTag(tag_) || PrivateModeEnabled() || !engine_ ||
      !engine_->context()) {
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
  std::vector<of<Candidate>> injected;
  const auto matches = store_.Query(active_input_, max_candidates_);
  injected.reserve(matches.size() + 2);
  if (!trimmed_input.empty()) {
    injected.push_back(
        New<YunPinSearchCandidate>(active_start_, active_end_, trimmed_input));
    injected.push_back(
        New<YunPinFavoriteCandidate>(active_start_, active_end_, trimmed_input));
  }

  if (matches.empty()) {
    if (injected.empty()) {
      return translation;
    }
    return New<YunPinMergedTranslation>(std::move(translation),
                                        std::move(injected));
  }

  std::set<std::string> seen;
  for (const auto& match : matches) {
    if (!seen.insert(match.text).second) {
      continue;
    }
    injected.push_back(New<YunPinCandidate>(match.id, active_start_,
                                            active_end_, match));
  }
  return New<YunPinMergedTranslation>(std::move(translation),
                                      std::move(injected));
}

}  // namespace rime
