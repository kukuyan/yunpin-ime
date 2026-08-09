// SPDX-License-Identifier: Apache-2.0
#pragma once

#include <cstddef>
#include <cstdint>
#include <string>
#include <string_view>
#include <vector>

namespace yunpin {

// A correction variant changes exactly one physical typing action inside one
// Pinyin syllable. The librime adapter validates `spelling` against the
// deployed Prism before exposing it to ScriptTranslator, so this portable
// generator never guesses Chinese text and never reads a dictionary itself.
enum class TypoEditKind : std::uint8_t {
  kNeighbourSubstitution,
  kAdjacentTransposition,
  kExtraKey,
  kMissingKey,
  kReviewedConfusion,
};

struct TypoVariant {
  std::string spelling;
  std::size_t consumed{0};
  std::uint8_t cost{0};
  TypoEditKind kind{TypoEditKind::kNeighbourSubstitution};
};

struct TypoCorrectionOptions {
  bool neighbour_substitution{true};
  bool adjacent_transposition{true};
  bool extra_key{true};
  bool missing_key{true};
  bool reviewed_confusions{true};
  // A 128-byte adversarial segment fails closed before variant generation.
  std::size_t max_input_bytes{128};
  std::size_t max_syllable_bytes{6};
  // 768 covers every one-letter insertion position for a six-letter Pinyin
  // syllable plus the bounded neighbour/transpose/extra-key variants.
  std::size_t max_variants{768};
};

// Generates deterministic, bounded correction spellings for the leading
// syllable of `remaining_input`. It accepts lowercase ASCII only, stops at a
// delimiter/non-letter, and never performs more than one edit in a variant.
// The caller must still discard spellings that do not exist in its Prism.
[[nodiscard]] std::vector<TypoVariant> GenerateTypoVariants(
    std::string_view remaining_input,
    const TypoCorrectionOptions& options = {});

}  // namespace yunpin
