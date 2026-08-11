// SPDX-License-Identifier: Apache-2.0
#pragma once

#include <rime/filter.h>

namespace rime {

// Controls whether the full-width-bracket Pinyin annotations produced by the
// YunPin schema are visible in the candidate window. The filter must run
// before `uniquifier`: its ShadowCandidate then remains transparent to
// Candidate::GetGenuineCandidate() during selection and learning.
class YunPinCommentFilter : public Filter {
 public:
  explicit YunPinCommentFilter(const Ticket& ticket);

  an<Translation> Apply(an<Translation> translation,
                        CandidateList* candidates) override;
};

}  // namespace rime
