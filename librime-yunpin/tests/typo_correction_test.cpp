// SPDX-License-Identifier: Apache-2.0
#include "yunpin/typo_correction.hpp"

#include <chrono>
#include <iostream>
#include <stdexcept>
#include <string>
#include <vector>

namespace {

using yunpin::TypoEditKind;
using yunpin::TypoVariant;

void Check(bool condition, const std::string& message) {
  if (!condition) {
    throw std::runtime_error(message);
  }
}

bool Contains(const std::vector<TypoVariant>& variants,
              const std::string& spelling,
              std::size_t consumed,
              TypoEditKind kind) {
  for (const TypoVariant& variant : variants) {
    if (variant.spelling == spelling && variant.consumed == consumed &&
        variant.kind == kind) {
      return true;
    }
  }
  return false;
}

void TestTouchTypingGoldens() {
  const auto neighbour = yunpin::GenerateTypoVariants(
      "xubijiaokuaideshihou");
  Check(Contains(neighbour, "su", 2,
                 TypoEditKind::kNeighbourSubstitution),
        "x must correct to its diagonal physical neighbour s");

  const auto missing = yunpin::GenerateTypoVariants(
      "shosubijiaokuaideshihou");
  Check(Contains(missing, "shou", 3, TypoEditKind::kMissingKey),
        "a missing u in shou must be recoverable locally");
  const auto missing_jiao = yunpin::GenerateTypoVariants(
      "jiakuaideshihou");
  Check(Contains(missing_jiao, "jiao", 3, TypoEditKind::kMissingKey),
        "the user's missing final o in jiao must be recoverable locally");
  const auto missing_tail = yunpin::GenerateTypoVariants(
      "zhuantai");
  Check(Contains(missing_tail, "zhuang", 5,
                 TypoEditKind::kMissingKey),
        "a missing final letter in a six-letter syllable must stay inside the bound");

  const auto extra = yunpin::GenerateTypoVariants(
      "shouusubijiaokuaideshihou");
  Check(Contains(extra, "shou", 5, TypoEditKind::kExtraKey),
        "one duplicated/extra key must be recoverable locally");

  const auto transposed = yunpin::GenerateTypoVariants(
      "shuosubijiaokuaideshihou");
  Check(Contains(transposed, "shou", 4,
                 TypoEditKind::kAdjacentTransposition),
        "an adjacent transposition inside one syllable must be recoverable");

  const auto conservative = yunpin::GenerateTypoVariants(
      "youjubeiyidingdejiucuolianxiangnengli");
  Check(!Contains(conservative, "yao", 3,
                  TypoEditKind::kReviewedConfusion),
        "valid-syllable confusion you -> yao must be off by default");

  yunpin::TypoCorrectionOptions reviewed_options;
  reviewed_options.reviewed_confusions = true;
  const auto reviewed = yunpin::GenerateTypoVariants(
      "youjubeiyidingdejiucuolianxiangnengli", reviewed_options);
  Check(Contains(reviewed, "yao", 3,
                 TypoEditKind::kReviewedConfusion),
        "an explicitly enabled reviewed confusion must remain available");
}

void TestBoundsAndExactInputNonPollution() {
  yunpin::TypoCorrectionOptions options;
  options.max_variants = 32;
  const auto bounded = yunpin::GenerateTypoVariants(
      "shouxubijiaokuaideshihou", options);
  Check(bounded.size() <= options.max_variants,
        "variant generation must obey its hard cap");
  for (const TypoVariant& variant : bounded) {
    Check(variant.spelling != "shou" || variant.consumed != 4,
          "the exact leading syllable must never be emitted as a correction");
  }

  Check(yunpin::GenerateTypoVariants("x").empty(),
        "single-letter input must not create correction noise");
  Check(yunpin::GenerateTypoVariants("Xu").empty(),
        "non-lowercase input must fail closed");
  Check(yunpin::GenerateTypoVariants(std::string(257, 'a')).empty(),
        "overlong input must fail closed");
  Check(yunpin::GenerateTypoVariants(std::string(128, 'a')).empty(),
        "the 128-byte adversarial boundary must fail closed");
}

void TestLongSentenceGenerationBudget() {
  const std::string input =
      "youjubeiyidingdejiucuolianxiangnengli";
  constexpr std::size_t kIterations = 10000;
  const auto start = std::chrono::steady_clock::now();
  std::size_t generated = 0;
  for (std::size_t index = 0; index < kIterations; ++index) {
    generated += yunpin::GenerateTypoVariants(input).size();
  }
  const auto elapsed = std::chrono::duration_cast<std::chrono::milliseconds>(
      std::chrono::steady_clock::now() - start);
  Check(generated > 0, "benchmark must exercise real variants");
  Check(elapsed < std::chrono::seconds(5),
        "portable correction generation must stay comfortably off the 20ms key budget");
}

}  // namespace

int main() {
  try {
    TestTouchTypingGoldens();
    TestBoundsAndExactInputNonPollution();
    TestLongSentenceGenerationBudget();
    std::cout << "typo_correction_tests: PASS\n";
    return 0;
  } catch (const std::exception& error) {
    std::cerr << "typo_correction_tests: FAIL: " << error.what() << '\n';
    return 1;
  }
}
