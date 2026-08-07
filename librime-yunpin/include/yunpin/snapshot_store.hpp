// SPDX-License-Identifier: Apache-2.0
#pragma once

#include <cstddef>
#include <istream>
#include <memory>
#include <string_view>
#include <vector>

#include "yunpin/phrase_engine.hpp"

namespace yunpin {

inline constexpr std::size_t kMaxPrivateSnapshotEntries = 50000;

struct SnapshotLoadResult {
  std::vector<PhraseEntry> entries;
  std::size_t accepted_rows{0};
  std::size_t rejected_rows{0};
  bool header_valid{false};
};

// Parses the importer's tab-separated private snapshot format. The first four
// columns are phrase, pinyin, source and use_count. An optional fifth `pinned`
// column accepts 1/true/yes/pinned. Invalid rows are counted but their private
// contents are never returned as diagnostics or written to logs.
[[nodiscard]] SnapshotLoadResult ParsePrivateSnapshot(std::istream& input);

// Owns one immutable index. Replace builds off the query path and publishes a
// shared_ptr with the C++17 atomic shared_ptr free functions. Query performs no
// filesystem or network operation.
class SnapshotStore {
 public:
  SnapshotStore();

  void Replace(std::vector<PhraseEntry> entries);
  [[nodiscard]] std::vector<Candidate> Query(std::string_view input,
                                             std::size_t limit = 2) const;
  [[nodiscard]] std::size_t size() const noexcept;

 private:
  std::shared_ptr<const PhraseIndex> index_;
};

}  // namespace yunpin
