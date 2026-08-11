// SPDX-License-Identifier: Apache-2.0
#include "rime_yunpin_comment_filter.hpp"

#include <cstdint>
#include <string>
#include <string_view>
#include <utility>

#include <rime/candidate.h>
#include <rime/context.h>
#include <rime/engine.h>
#include <rime/translation.h>

namespace rime {
namespace {

constexpr std::string_view kShowPinyinOption =
    "yunpin_show_candidate_pinyin";
constexpr std::string_view kFullWidthLeftBracket = "\xEF\xBC\xBB";
constexpr std::string_view kFullWidthRightBracket = "\xEF\xBC\xBD";

bool IsPinyinBody(std::string_view body) noexcept {
  bool has_latin_letter = false;
  for (std::size_t offset = 0; offset < body.size();) {
    const unsigned char first = static_cast<unsigned char>(body[offset]);
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
    } else {
      return false;
    }
    if (offset + width > body.size()) {
      return false;
    }
    for (std::size_t index = 1; index < width; ++index) {
      const unsigned char continuation =
          static_cast<unsigned char>(body[offset + index]);
      if ((continuation & 0xc0) != 0x80) {
        return false;
      }
      codepoint = (codepoint << 6) | (continuation & 0x3f);
    }
    if ((width > 1 && codepoint < minimum) ||
        (codepoint >= 0xd800 && codepoint <= 0xdfff)) {
      return false;
    }

    const bool ascii_letter =
        (codepoint >= 'A' && codepoint <= 'Z') ||
        (codepoint >= 'a' && codepoint <= 'z');
    const bool latin_letter =
        ascii_letter ||
        (codepoint >= 0x00c0 && codepoint <= 0x00ff &&
         codepoint != 0x00d7 && codepoint != 0x00f7) ||
        (codepoint >= 0x0100 && codepoint <= 0x017f) ||
        (codepoint >= 0x1e00 && codepoint <= 0x1eff);
    const bool combining_mark = codepoint >= 0x0300 && codepoint <= 0x036f;
    const bool separator = codepoint == ' ' || codepoint == '\'' ||
                           codepoint == '-' || codepoint == ':' ||
                           (codepoint >= '0' && codepoint <= '5');
    if (!latin_letter && !combining_mark && !separator) {
      return false;
    }
    has_latin_letter = has_latin_letter || latin_letter;
    offset += width;
  }
  return has_latin_letter;
}

bool IsPinyinAnnotation(std::string_view comment) noexcept {
  const std::size_t framing_size =
      kFullWidthLeftBracket.size() + kFullWidthRightBracket.size();
  if (comment.size() <= framing_size ||
      comment.compare(0, kFullWidthLeftBracket.size(),
                      kFullWidthLeftBracket) != 0 ||
      comment.compare(comment.size() - kFullWidthRightBracket.size(),
                      kFullWidthRightBracket.size(),
                      kFullWidthRightBracket) != 0) {
    return false;
  }
  return IsPinyinBody(comment.substr(
      kFullWidthLeftBracket.size(), comment.size() - framing_size));
}

an<Candidate> UnwrapShadowCandidates(an<Candidate> candidate) {
  // librime's GetGenuineCandidate() intentionally unwraps one shadow layer.
  // A filter pipeline may already have created more than one, so flatten the
  // complete chain before adding YunPin's display-only wrapper.
  while (const auto shadow = As<ShadowCandidate>(candidate)) {
    candidate = shadow->item();
  }
  return candidate;
}

an<Candidate> MaskPinyinAnnotation(const an<Candidate>& candidate) {
  const auto genuine = UnwrapShadowCandidates(candidate);
  if (!genuine) {
    return candidate;
  }

  // Preserve what the preceding filters made visible while wrapping the
  // genuine candidate directly. `inherit_comment = false` is the only display
  // change and keeps selection/commit/learning bound to `genuine`.
  auto masked = New<ShadowCandidate>(genuine, candidate->type(),
                                     candidate->text(), std::string(), false);
  // Filters are allowed to refine bounds and quality on a ShadowCandidate
  // after construction, so copy the currently visible metadata rather than
  // assuming it still equals the genuine item's values.
  masked->set_start(candidate->start());
  masked->set_end(candidate->end());
  masked->set_quality(candidate->quality());
  // Candidate's correction bit is metadata added by YunPin's version-locked
  // librime patch. ShadowCandidate does not inherit it, so copy it explicitly
  // to keep this display-only filter neutral to downstream ranking guards.
  masked->set_correction(candidate->is_correction());
  return masked;
}

class YunPinCommentVisibilityTranslation : public Translation {
 public:
  explicit YunPinCommentVisibilityTranslation(an<Translation> upstream)
      : upstream_(std::move(upstream)) {
    RefreshExhausted();
  }

  bool Next() override {
    if (!upstream_ || upstream_->exhausted()) {
      set_exhausted(true);
      return false;
    }
    upstream_->Next();
    RefreshExhausted();
    return !exhausted();
  }

  an<Candidate> Peek() override {
    if (!upstream_ || upstream_->exhausted()) {
      return nullptr;
    }
    const auto candidate = upstream_->Peek();
    if (!candidate || !IsPinyinAnnotation(candidate->comment())) {
      return candidate;
    }
    return MaskPinyinAnnotation(candidate);
  }

 private:
  void RefreshExhausted() {
    set_exhausted(!upstream_ || upstream_->exhausted());
  }

  an<Translation> upstream_;
};

}  // namespace

YunPinCommentFilter::YunPinCommentFilter(const Ticket& ticket)
    : Filter(ticket) {}

an<Translation> YunPinCommentFilter::Apply(an<Translation> translation,
                                            CandidateList* candidates) {
  (void)candidates;
  const Context* context = engine_ ? engine_->context() : nullptr;
  const bool show_pinyin =
      context && context->get_option(std::string(kShowPinyinOption));
  if (show_pinyin || !translation) {
    return translation;
  }
  return New<YunPinCommentVisibilityTranslation>(std::move(translation));
}

}  // namespace rime
