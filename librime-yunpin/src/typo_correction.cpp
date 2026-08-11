// SPDX-License-Identifier: Apache-2.0
#include "yunpin/typo_correction.hpp"

#include <algorithm>
#include <array>
#include <string>
#include <unordered_map>
#include <utility>

namespace yunpin {
namespace {

constexpr std::uint8_t kNeighbourCost = 1;
constexpr std::uint8_t kStructuralEditCost = 2;
constexpr std::uint8_t kExtraKeyCost = 3;
// A reviewed one-way pair is more trustworthy than an arbitrary inserted
// letter. This priority also prevents a shorter prefix such as `yo` + `a`
// from stealing the same canonical syllable from the full `you` -> `yao`
// correction. ScriptTranslator still applies its normal correction penalty.
constexpr std::uint8_t kReviewedConfusionCost = 1;

// Physical QWERTY neighbours include the diagonal keys used during fast touch
// typing. In particular, x<->s is intentionally present for the user's
// high-frequency shouxu -> shousu error. The table is symmetric and excludes
// keys more than one key-width away.
constexpr std::array<std::string_view, 26> kPhysicalNeighbours = {
    /* a */ "qwsz",   /* b */ "ghvn",   /* c */ "dfxv",
    /* d */ "ersfxc", /* e */ "wrsd",   /* f */ "rtdgcv",
    /* g */ "tyfhvb", /* h */ "yugjbn", /* i */ "uojk",
    /* j */ "uihknm", /* k */ "iojlm",  /* l */ "opk",
    /* m */ "jkn",    /* n */ "hjbm",   /* o */ "ipkl",
    /* p */ "ol",     /* q */ "wa",     /* r */ "etdf",
    /* s */ "weadzx", /* t */ "ryfg",   /* u */ "yihj",
    /* v */ "fgcb",   /* w */ "qeas",   /* x */ "sdzc",
    /* y */ "tugh",   /* z */ "asx",
};

struct ReviewedConfusion {
  std::string_view typed;
  std::string_view intended;
};

// Valid-syllable to valid-syllable substitutions cannot be discovered by an
// invalid-spelling search. Keep this list deliberately small and one-way;
// sentence/dictionary scoring still applies librime's correction penalty.
constexpr std::array<ReviewedConfusion, 1> kReviewedConfusions = {{
    {"you", "yao"},
}};

bool IsLowerAscii(char value) noexcept {
  return value >= 'a' && value <= 'z';
}

std::size_t LeadingLowerAscii(std::string_view input,
                              std::size_t limit) noexcept {
  std::size_t length = 0;
  while (length < input.size() && length < limit &&
         IsLowerAscii(input[length])) {
    ++length;
  }
  return length;
}

std::string VariantKey(std::string_view spelling, std::size_t consumed) {
  std::string key;
  key.reserve(spelling.size() + 1 + sizeof(consumed));
  key.append(spelling);
  key.push_back('\0');
  key.append(reinterpret_cast<const char*>(&consumed), sizeof(consumed));
  return key;
}

}  // namespace

std::vector<TypoVariant> GenerateTypoVariants(
    std::string_view remaining_input, const TypoCorrectionOptions& options) {
  std::vector<TypoVariant> result;
  if (remaining_input.empty() ||
      remaining_input.size() >= options.max_input_bytes ||
      options.max_syllable_bytes < 2 || options.max_variants == 0) {
    return result;
  }

  const std::size_t leading = LeadingLowerAscii(
      remaining_input, options.max_syllable_bytes + 1);
  if (leading < 2) {
    return result;
  }

  result.reserve(std::min<std::size_t>(options.max_variants, 256));
  std::unordered_map<std::string, std::size_t> seen;
  seen.reserve(std::min<std::size_t>(options.max_variants, 512));

  const auto add = [&](std::string spelling, std::size_t consumed,
                       std::uint8_t cost, TypoEditKind kind) {
    if (result.size() >= options.max_variants || spelling.size() < 2 ||
        spelling.size() > options.max_syllable_bytes || consumed < 2 ||
        consumed > leading) {
      return;
    }
    const std::string key = VariantKey(spelling, consumed);
    const auto existing = seen.find(key);
    if (existing == seen.end()) {
      seen.emplace(key, result.size());
      result.push_back(
          TypoVariant{std::move(spelling), consumed, cost, kind});
      return;
    }
    TypoVariant& previous = result[existing->second];
    if (cost < previous.cost) {
      previous.cost = cost;
      previous.kind = kind;
    }
  };

  if (options.reviewed_confusions) {
    for (const ReviewedConfusion& confusion : kReviewedConfusions) {
      if (remaining_input.size() >= confusion.typed.size() &&
          remaining_input.compare(0, confusion.typed.size(),
                                  confusion.typed) == 0) {
        add(std::string(confusion.intended), confusion.typed.size(),
            kReviewedConfusionCost, TypoEditKind::kReviewedConfusion);
      }
    }
  }

  const std::size_t same_length_limit =
      std::min(leading, options.max_syllable_bytes);
  for (std::size_t length = 2; length <= same_length_limit; ++length) {
    const std::string prefix(remaining_input.substr(0, length));
    if (options.neighbour_substitution) {
      for (std::size_t position = 0; position < length; ++position) {
        const std::string_view neighbours =
            kPhysicalNeighbours[static_cast<std::size_t>(prefix[position] - 'a')];
        for (const char replacement : neighbours) {
          std::string spelling = prefix;
          spelling[position] = replacement;
          add(std::move(spelling), length, kNeighbourCost,
              TypoEditKind::kNeighbourSubstitution);
        }
      }
    }
    if (options.adjacent_transposition) {
      for (std::size_t position = 0; position + 1 < length; ++position) {
        if (prefix[position] == prefix[position + 1]) {
          continue;
        }
        std::string spelling = prefix;
        std::swap(spelling[position], spelling[position + 1]);
        add(std::move(spelling), length, kStructuralEditCost,
            TypoEditKind::kAdjacentTransposition);
      }
    }
  }

  if (options.extra_key) {
    const std::size_t typed_limit =
        std::min(leading, options.max_syllable_bytes + 1);
    for (std::size_t length = 3; length <= typed_limit; ++length) {
      const std::string prefix(remaining_input.substr(0, length));
      for (std::size_t position = 0; position < length; ++position) {
        std::string spelling = prefix;
        spelling.erase(position, 1);
        add(std::move(spelling), length, kExtraKeyCost,
            TypoEditKind::kExtraKey);
      }
    }
  }

  if (options.missing_key) {
    const std::size_t typed_limit =
        std::min(leading, options.max_syllable_bytes - 1);
    for (std::size_t length = 2; length <= typed_limit; ++length) {
      const std::string prefix(remaining_input.substr(0, length));
      for (std::size_t position = 0; position <= length; ++position) {
        for (char inserted = 'a'; inserted <= 'z'; ++inserted) {
          std::string spelling = prefix;
          spelling.insert(position, 1, inserted);
          add(std::move(spelling), length, kStructuralEditCost,
              TypoEditKind::kMissingKey);
        }
      }
    }
  }

  return result;
}

}  // namespace yunpin
