// SPDX-License-Identifier: Apache-2.0
#pragma once

#include <cstddef>
#include <memory>
#include <string>
#include <vector>

#include <rime/filter.h>

#include "yunpin/session_learning.hpp"
#include "yunpin/snapshot_store.hpp"

namespace rime {

class Context;
class KeyEvent;
class YunPinSessionLearningBridge;

class YunPinFilter : public Filter {
 public:
  explicit YunPinFilter(const Ticket& ticket);
  ~YunPinFilter() override;

  an<Translation> Apply(an<Translation> translation,
                        CandidateList* candidates) override;
  bool AppliesToSegment(Segment* segment) override;
  [[nodiscard]] std::vector<yunpin::HabitStat> QueryHabits(
      const yunpin::HabitQuery& query = {}) const;

 private:
  bool LoadSnapshot(const std::string& relative_path);
  bool PrivateModeEnabled() const;
  void DisconnectLearningNotifiers() noexcept;
  // True while either the private overlay or the conservative short-input
  // guard has work to do. Expression actions remain disconnected.
  bool Active() const;

  yunpin::SnapshotStore store_;
  std::string tag_{"abc"};
  std::string snapshot_path_{"yunpin/private.tsv"};
  std::string active_input_;
  std::size_t active_start_{0};
  std::size_t active_end_{0};
  std::size_t max_candidates_{2};
  std::size_t long_correction_min_chars_{12};
  // `enabled` gates private snapshot loading/injection. The short-input guard
  // is independent so the Windows preview can reject implausible upstream
  // predictions without enabling private data or session learning in a TSF
  // host.
  bool enabled_{true};
  bool short_input_guard_{true};
  // When enabled by the schema, a spelling-correction candidate for a long
  // composition cannot displace personal or ordinary candidates. At most one
  // automatic correction may occupy total rank two or three; all remaining
  // correction candidates fail closed instead of spilling onto later pages.
  bool long_correction_guard_{false};
  // Fail closed unless a reviewed platform overlay explicitly enables
  // learning. Both shipped overlays remain false until their trusted
  // secure/private-field bridges and real-host lifecycle tests pass.
  bool session_learning_enabled_{false};
  bool private_ready_{false};
  // Callbacks lock a weak reference to this state instead of capturing the
  // filter.  This matters when an IMK/TSF host tears down a session from a
  // nested notifier or while an old Menu still owns a filtered translation.
  std::shared_ptr<YunPinSessionLearningBridge> session_learning_;
  connection commit_connection_;
  connection update_connection_;
  connection unhandled_key_connection_;
  connection option_update_connection_;
  connection delete_connection_;
};

}  // namespace rime
