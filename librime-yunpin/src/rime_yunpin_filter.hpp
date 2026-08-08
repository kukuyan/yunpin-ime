// SPDX-License-Identifier: Apache-2.0
#pragma once

#include <cstddef>
#include <memory>
#include <string>
#include <vector>

#include <rime/filter.h>

#include "yunpin/snapshot_store.hpp"

namespace rime {

class YunPinFilter : public Filter {
 public:
  explicit YunPinFilter(const Ticket& ticket);

  an<Translation> Apply(an<Translation> translation,
                        CandidateList* candidates) override;
  bool AppliesToSegment(Segment* segment) override;

 private:
  bool LoadSnapshot(const std::string& relative_path);
  bool PrivateModeEnabled() const;
  // True when the filter has anything to contribute: either a loaded private
  // snapshot or the opt-in expression actions.
  bool Active() const;
  std::size_t PageSize() const;

  yunpin::SnapshotStore store_;
  std::string tag_{"abc"};
  std::string snapshot_path_{"yunpin/private.tsv"};
  std::string active_input_;
  std::size_t active_start_{0};
  std::size_t active_end_{0};
  std::size_t max_candidates_{2};
  bool enabled_{true};
  // The expression actions reach the network and the local disk, so they are
  // off unless a deployment opts in. They are deliberately independent of the
  // private snapshot: the two features share no data.
  bool expression_search_{false};
  bool private_ready_{false};
};

}  // namespace rime
