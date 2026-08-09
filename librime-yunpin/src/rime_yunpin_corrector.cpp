// SPDX-License-Identifier: Apache-2.0
#include "rime_yunpin_corrector.hpp"

#include <algorithm>
#include <limits>
#include <unordered_map>
#include <vector>

#include <rime/schema.h>
#include <rime/ticket.h>

namespace rime {
namespace {

// This is an edge budget, not merely a display quota. Keeping it small bounds
// the syllable graph before Dictionary/Table traversal while still leaving
// room for the handful of plausible one-edit Pinyin corrections at an input
// offset.
constexpr std::size_t kMaxCorrectionsPerOffset = 16;

bool ConfigBool(const Ticket& ticket,
                const char* key,
                bool default_value) {
  bool value = default_value;
  if (ticket.schema && ticket.schema->config()) {
    ticket.schema->config()->GetBool(key, &value);
  }
  return value;
}

std::size_t BoundaryEvidence(const Prism& prism,
                             const string& key,
                             std::size_t consumed) {
  if (consumed == key.size()) {
    return 7;  // stronger than the longest standard Pinyin syllable
  }
  if (consumed > key.size()) {
    return 0;
  }
  const string remaining = key.substr(consumed);
  vector<Prism::Match> matches;
  // CommonPrefixSearch is logically read-only even though the pinned librime
  // 1.16/1.17 signature predates a const qualifier.
  Prism& mutable_prism = const_cast<Prism&>(prism);
  mutable_prism.CommonPrefixSearch(remaining, &matches);
  std::size_t best = 0;
  for (const Prism::Match& match : matches) {
    for (auto accessor = mutable_prism.QuerySpelling(match.value);
         !accessor.exhausted(); accessor.Next()) {
      const SpellingProperties properties = accessor.properties();
      if (properties.type == kNormalSpelling &&
          !properties.is_correction) {
        best = std::max(best, match.length);
        break;
      }
    }
  }
  return best;
}

struct SelectedVariant {
  const yunpin::TypoVariant* variant{nullptr};
  std::size_t boundary_evidence{0};
};

struct RankedVariant {
  int spelling_id{-1};
  SelectedVariant selected;
};

bool BetterVariant(const SelectedVariant& candidate,
                   const SelectedVariant& current) {
  if (!current.variant) {
    return true;
  }
  if (candidate.boundary_evidence != current.boundary_evidence) {
    return candidate.boundary_evidence > current.boundary_evidence;
  }
  if (candidate.variant->cost != current.variant->cost) {
    return candidate.variant->cost < current.variant->cost;
  }
  return candidate.variant->consumed < current.variant->consumed;
}

bool BetterRankedVariant(const RankedVariant& candidate,
                         const RankedVariant& current) {
  if (BetterVariant(candidate.selected, current.selected)) {
    return true;
  }
  if (BetterVariant(current.selected, candidate.selected)) {
    return false;
  }
  return candidate.spelling_id < current.spelling_id;
}

}  // namespace

YunPinCorrector::YunPinCorrector(const Ticket& ticket) {
  options_.neighbour_substitution =
      ConfigBool(ticket, "yunpin/typo_neighbour_keys", true);
  options_.adjacent_transposition =
      ConfigBool(ticket, "yunpin/typo_transposition", true);
  options_.extra_key = ConfigBool(ticket, "yunpin/typo_extra_key", true);
  options_.missing_key = ConfigBool(ticket, "yunpin/typo_missing_key", true);
  options_.reviewed_confusions =
      ConfigBool(ticket, "yunpin/typo_reviewed_confusions", true);
}

void YunPinCorrector::ToleranceSearch(
    const Prism& prism, const string& key,
    corrector::Corrections* results, size_t tolerance) {
  if (!results || key.empty() || tolerance == 0) {
    return;
  }
  const auto variants = yunpin::GenerateTypoVariants(key, options_);
  std::unordered_map<int, SelectedVariant> selected;
  selected.reserve(64);
  for (const yunpin::TypoVariant& variant : variants) {
    if (variant.cost > tolerance ||
        variant.consumed >
            static_cast<std::size_t>(std::numeric_limits<int>::max())) {
      continue;
    }
    int spelling_id = -1;
    if (!prism.GetValue(variant.spelling, &spelling_id) || spelling_id < 0) {
      continue;
    }
    const SelectedVariant candidate{
        &variant, BoundaryEvidence(prism, key, variant.consumed)};
    SelectedVariant& current = selected[spelling_id];
    if (BetterVariant(candidate, current)) {
      current = candidate;
    }
  }
  std::vector<RankedVariant> ranked;
  ranked.reserve(selected.size());
  for (const auto& entry : selected) {
    ranked.push_back({entry.first, entry.second});
  }
  std::sort(ranked.begin(), ranked.end(), BetterRankedVariant);
  if (ranked.size() > kMaxCorrectionsPerOffset) {
    ranked.resize(kMaxCorrectionsPerOffset);
  }
  for (const RankedVariant& entry : ranked) {
    const int spelling_id = entry.spelling_id;
    const yunpin::TypoVariant& variant = *entry.selected.variant;
    results->Alter(spelling_id,
                   {variant.cost, spelling_id, variant.consumed});
  }
}

Corrector* YunPinCorrectorComponent::Create(const Ticket& ticket) noexcept {
  if (!ConfigBool(ticket, "yunpin/typo_correction", false)) {
    return new NearSearchCorrector();
  }
  return new YunPinCorrector(ticket);
}

}  // namespace rime
