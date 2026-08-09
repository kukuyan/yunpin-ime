// SPDX-License-Identifier: Apache-2.0
#pragma once

#include <atomic>
#include <cstddef>
#include <cstdint>
#include <memory>
#include <string>
#include <string_view>
#include <vector>

namespace yunpin {

// Four or more syllables use the conservative long-phrase recall gate. Short
// pinned phrases remain available from their ordinary two-letter initials.
inline constexpr std::size_t kLongPhraseMinSyllables = 4;

enum class PhraseOrigin : std::uint8_t {
  kBase,
  kPublic,
  kHistory,
  kImported,
  kPersonal,
};

enum class MatchKind : std::uint8_t {
  kInitials,
  kFullPrefix,
  kExactFull,
};

struct PhraseEntry {
  std::string id;
  std::string text;
  std::vector<std::string> syllables;
  PhraseOrigin origin{PhraseOrigin::kBase};
  std::uint64_t use_count{0};
  std::int64_t static_weight{0};
  bool pinned{false};
  bool learned{false};
  bool tombstoned{false};
  // Persisted aggregate from explicit correction events. Ordinary use_count
  // remains a separate signal so an accidental choice cannot be disguised as
  // repeated intentional use.
  std::int32_t correction_score{0};
};

struct Candidate {
  std::string id;
  std::string text;
  std::string full_pinyin;
  std::string initials;
  PhraseOrigin origin{PhraseOrigin::kBase};
  MatchKind match{MatchKind::kInitials};
  std::uint64_t use_count{0};
  std::int64_t static_weight{0};
  bool pinned{false};
  std::int32_t correction_score{0};

  [[nodiscard]] bool is_personal() const noexcept;
};

struct SegmenterLimits {
  std::size_t max_paths{16};
  std::size_t max_syllables_per_path{32};
  std::size_t max_input_letters{128};
};

// Bounded dictionary segmenter for complete ASCII pinyin. It returns ambiguous
// paths in deterministic longest-syllable-first order. Apostrophes, whitespace,
// and hyphens are hard boundaries; for example, xian can produce [xian] and
// [xi, an], while xi'an cannot cross the explicit boundary to produce [xian].
class PinyinSegmenter {
 public:
  explicit PinyinSegmenter(SegmenterLimits limits = {});

  [[nodiscard]] std::vector<std::vector<std::string>> Segment(
      std::string_view input) const;
  [[nodiscard]] bool IsSyllable(std::string_view syllable) const;

 private:
  [[nodiscard]] std::vector<std::vector<std::string>> SegmentChunk(
      std::string_view chunk, bool explicit_boundary) const;

  SegmenterLimits limits_;
  std::vector<std::string> syllables_;
  std::size_t max_syllable_length_{0};
};

// Every fuzzy pair is opt-in. Common() enables the conventional set while
// max_aliases keeps expansion bounded. Expansion is disabled for one- and
// two-letter input and for text that cannot be segmented into complete pinyin
// syllables.
struct FuzzyConfig {
  bool zh_z{false};
  bool ch_c{false};
  bool sh_s{false};
  bool n_l{false};
  bool en_eng{false};
  bool in_ing{false};
  std::size_t max_aliases{16};

  [[nodiscard]] bool enabled() const noexcept;
  [[nodiscard]] static FuzzyConfig Common();
};

// The normalized literal input is always the first result. Additional aliases
// are complete, dictionary-validated pinyin paths and never exceed
// config.max_aliases.
[[nodiscard]] std::vector<std::string> ExpandFuzzyAliases(
    std::string_view input, const FuzzyConfig& config);
[[nodiscard]] std::vector<std::string> ExpandFuzzyAliases(
    std::string_view input, const FuzzyConfig& config,
    const PinyinSegmenter& segmenter);

// Immutable lookup keys and lock-free atomic tombstone bits are kept in memory.
// Desktop adapters can build a replacement PhraseIndex in the background and
// atomically swap their shared_ptr; Query performs no disk or network access.
class PhraseIndex {
 public:
  explicit PhraseIndex(std::vector<PhraseEntry> entries);
  PhraseIndex(std::vector<PhraseEntry> entries, FuzzyConfig fuzzy_config);

  // Returns at most limit candidates. The first eight positions contain at most
  // two personal/history/import candidates. If there are not enough public/base
  // candidates to fill that page, the result deliberately contains fewer than
  // eight entries rather than violating the privacy/pollution quota.
  [[nodiscard]] std::vector<Candidate> Query(std::string_view input,
                                             std::size_t limit = 9) const;

  // Applies a local remove-wins tombstone. This operation is safe to call while
  // other threads query the index. A background index replacement can later
  // compact tombstoned records after all devices have observed them.
  [[nodiscard]] bool ApplyTombstone(std::string_view id);

  // Applies an explicit correction delta without rebuilding the immutable
  // lookup keys. Negative feedback demotes the just-undone candidate and
  // positive feedback promotes its immediate replacement. A successful
  // update advances revision(), allowing desktop adapters to reject any
  // candidate menu cached before the correction.
  [[nodiscard]] bool ApplyCorrectionFeedback(std::string_view id,
                                             std::int32_t delta);

  [[nodiscard]] std::uint32_t revision() const noexcept;
  [[nodiscard]] bool CanReuseRevision(
      std::uint32_t cached_revision) const noexcept;

  [[nodiscard]] std::size_t size() const noexcept;

 private:
  struct IndexedEntry {
    PhraseEntry entry;
    std::string full_pinyin;
    std::string initials;
  };

  struct KeyRef {
    std::string key;
    std::size_t entry_index{0};
  };

  std::vector<IndexedEntry> entries_;
  std::vector<KeyRef> full_index_;
  std::vector<KeyRef> initials_index_;
  std::unique_ptr<std::atomic_bool[]> tombstones_;
  std::unique_ptr<std::atomic<std::int32_t>[]> correction_scores_;
  std::atomic<std::uint32_t> revision_{0};
  FuzzyConfig fuzzy_config_;
};

// Accepts ASCII pinyin separated by whitespace or apostrophes. Input is
// lower-cased and separators are removed for lookup. "u:" is canonicalized to
// "v", matching the conventional keyboard spelling for ü.
[[nodiscard]] std::string NormalizePinyin(std::string_view input);

[[nodiscard]] std::vector<std::string> SplitPinyin(std::string_view input);

}  // namespace yunpin
