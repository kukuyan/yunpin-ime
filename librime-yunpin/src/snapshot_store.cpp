// SPDX-License-Identifier: Apache-2.0
#include "yunpin/snapshot_store.hpp"

#include <algorithm>
#include <array>
#include <charconv>
#include <cstdint>
#include <iomanip>
#include <limits>
#include <memory>
#include <sstream>
#include <string>
#include <unordered_set>
#include <utility>

namespace yunpin {
namespace {

constexpr std::size_t kMaxPhraseBytes = 512;
constexpr std::size_t kMaxPinyinBytes = 256;
constexpr std::size_t kMaxSourceBytes = 128;

std::vector<std::string> SplitTabs(const std::string& line) {
  std::vector<std::string> fields;
  std::size_t start = 0;
  while (start <= line.size()) {
    const std::size_t tab = line.find('\t', start);
    if (tab == std::string::npos) {
      fields.emplace_back(line.substr(start));
      break;
    }
    fields.emplace_back(line.substr(start, tab - start));
    start = tab + 1;
  }
  return fields;
}

std::string LowerAscii(std::string value) {
  std::transform(value.begin(), value.end(), value.begin(), [](char ch) {
    if (ch >= 'A' && ch <= 'Z') {
      return static_cast<char>(ch - 'A' + 'a');
    }
    return ch;
  });
  return value;
}

bool ParseCount(std::string_view text, std::uint64_t* count) {
  if (text.empty() || count == nullptr) {
    return false;
  }
  std::uint64_t parsed = 0;
  const char* begin = text.data();
  const char* end = text.data() + text.size();
  const auto result = std::from_chars(begin, end, parsed);
  if (result.ec != std::errc() || result.ptr != end || parsed == 0) {
    return false;
  }
  *count = parsed;
  return true;
}

bool ParseLastUsedDay(std::string_view text, std::int64_t* day) {
  if (text.empty() || day == nullptr) {
    return false;
  }
  std::int64_t parsed = 0;
  const char* begin = text.data();
  const char* end = text.data() + text.size();
  const auto result = std::from_chars(begin, end, parsed);
  if (result.ec != std::errc() || result.ptr != end || parsed < 0) {
    return false;
  }
  *day = parsed;
  return true;
}

bool ParseLearningSourceDay(std::string_view source, std::int64_t* day,
                            bool* present) {
  if (day == nullptr || present == nullptr) {
    return false;
  }
  *day = 0;
  *present = false;
  constexpr std::string_view prefix = "synced_learning@";
  if (source.size() < prefix.size() ||
      source.substr(0, prefix.size()) != prefix) {
    return true;
  }
  *present = true;
  return ParseLastUsedDay(source.substr(prefix.size()), day) && *day > 0;
}

bool ParsePinned(std::string value) {
  value = LowerAscii(std::move(value));
  return value == "1" || value == "true" || value == "yes" ||
         value == "pinned";
}

PhraseOrigin OriginForSource(std::string source) {
  source = LowerAscii(std::move(source));
  if (source.find("history") != std::string::npos ||
      source.find("chatgpt") != std::string::npos ||
      source.find("codex") != std::string::npos) {
    return PhraseOrigin::kHistory;
  }
  if (source.find("sogou") != std::string::npos ||
      source.find("scel") != std::string::npos ||
      source.find("sgpybin") != std::string::npos ||
      source.find("import") != std::string::npos) {
    return PhraseOrigin::kImported;
  }
  return PhraseOrigin::kPersonal;
}

std::string StableId(std::string_view phrase, std::string_view pinyin) {
  std::uint64_t hash = 1469598103934665603ULL;
  const auto append = [&hash](std::string_view value) {
    for (const unsigned char byte : value) {
      hash ^= byte;
      hash *= 1099511628211ULL;
    }
  };
  append(phrase);
  hash ^= 0xff;
  hash *= 1099511628211ULL;
  append(pinyin);

  std::ostringstream id;
  id << "yp-" << std::hex << std::setw(16) << std::setfill('0') << hash;
  return id.str();
}

bool IsExpectedHeader(const std::vector<std::string>& fields) {
  return fields.size() >= 4 && fields[0] == "phrase" &&
         fields[1] == "pinyin" && fields[2] == "source" &&
         fields[3] == "use_count" &&
         (fields.size() == 4 ||
          (fields.size() == 5 && fields[4] == "pinned") ||
          (fields.size() == 6 && fields[4] == "pinned" &&
           fields[5] == "last_used_day"));
}

bool IsLegacyPrivateSnapshotToken(std::string_view syllable) {
  // Long-lived personal dictionaries can contain an explicitly separated
  // Latin initial (for example a product or acronym letter). Keep that legacy
  // code private to this snapshot loader; do not add it to the process-wide
  // PinyinSegmenter, fuzzy aliases, public dictionaries or typo correction.
  if (syllable.size() == 1 && syllable.front() >= 'a' &&
      syllable.front() <= 'z') {
    return true;
  }

  // These finite, tone-free spellings are emitted by the reviewed legacy
  // personal-dictionary source but are outside the standard Hanyu Pinyin set.
  // An exact allowlist avoids accepting arbitrary alphabetic pseudo-syllables.
  static constexpr std::array<std::string_view, 3> kLegacyPrivateSpellings = {
      "fiao", "kei", "tei"};
  return std::binary_search(kLegacyPrivateSpellings.begin(),
                            kLegacyPrivateSpellings.end(), syllable);
}

}  // namespace

SnapshotLoadResult ParsePrivateSnapshot(std::istream& input) {
  SnapshotLoadResult result;
  std::string line;
  if (!std::getline(input, line)) {
    return result;
  }
  if (!line.empty() && line.back() == '\r') {
    line.pop_back();
  }
  const std::vector<std::string> header = SplitTabs(line);
  result.header_valid = IsExpectedHeader(header);
  if (!result.header_valid) {
    return result;
  }
  const bool has_pinned = header.size() >= 5;
  const bool has_last_used_day = header.size() == 6;
  const PinyinSegmenter segmenter;
  std::unordered_set<std::string> ids;
  ids.reserve(kMaxPrivateSnapshotEntries);

  while (std::getline(input, line)) {
    if (!line.empty() && line.back() == '\r') {
      line.pop_back();
    }
    if (line.empty() || line.front() == '#') {
      continue;
    }
    if (result.entries.size() >= kMaxPrivateSnapshotEntries) {
      ++result.rejected_rows;
      continue;
    }

    const std::vector<std::string> fields = SplitTabs(line);
    if (fields.size() != header.size() || fields[0].empty() ||
        fields[0].size() > kMaxPhraseBytes || fields[1].empty() ||
        fields[1].size() > kMaxPinyinBytes ||
        fields[2].size() > kMaxSourceBytes) {
      ++result.rejected_rows;
      continue;
    }

    std::uint64_t use_count = 0;
    std::int64_t last_used_day = 0;
    std::int64_t source_last_used_day = 0;
    bool source_has_last_used_day = false;
    const std::vector<std::string> syllables = SplitPinyin(fields[1]);
    bool all_syllables_valid = !syllables.empty();
    bool private_exact_code_only = false;
    for (const std::string& syllable : syllables) {
      if (segmenter.IsSyllable(syllable)) {
        continue;
      }
      if (IsLegacyPrivateSnapshotToken(syllable)) {
        private_exact_code_only = true;
        continue;
      }
      all_syllables_valid = false;
      break;
    }
    if (private_exact_code_only && NormalizePinyin(fields[1]).size() < 2) {
      // A one-letter private code is too broad to distinguish from ordinary
      // typing intent. Keep the same minimum as the personal-initial recall
      // gate instead of letting compatibility bypass the short-input guard.
      all_syllables_valid = false;
    }
    if (!all_syllables_valid || !ParseCount(fields[3], &use_count) ||
        !ParseLearningSourceDay(fields[2], &source_last_used_day,
                                &source_has_last_used_day) ||
        (has_last_used_day &&
         !ParseLastUsedDay(fields[5], &last_used_day)) ||
        (has_last_used_day && source_has_last_used_day &&
         last_used_day != source_last_used_day)) {
      ++result.rejected_rows;
      continue;
    }
    if (!has_last_used_day && source_has_last_used_day) {
      last_used_day = source_last_used_day;
    }

    const std::string id = StableId(fields[0], fields[1]);
    if (!ids.insert(id).second) {
      ++result.rejected_rows;
      continue;
    }

    PhraseEntry entry;
    entry.id = id;
    entry.text = fields[0];
    entry.syllables = syllables;
    entry.origin = OriginForSource(fields[2]);
    entry.use_count = use_count;
    entry.static_weight = 0;
    entry.pinned = has_pinned && ParsePinned(fields[4]);
    entry.learned = use_count >= kAutomaticLearningThreshold;
    entry.private_exact_code_only = private_exact_code_only;
    entry.last_used_day = last_used_day;
    result.entries.push_back(std::move(entry));
    ++result.accepted_rows;
  }
  return result;
}

SnapshotStore::SnapshotStore()
    : index_(std::make_shared<const PhraseIndex>(
          std::vector<PhraseEntry>{})) {}

void SnapshotStore::Replace(std::vector<PhraseEntry> entries) {
  if (entries.size() > kMaxPrivateSnapshotEntries) {
    entries.resize(kMaxPrivateSnapshotEntries);
  }
  auto replacement =
      std::make_shared<const PhraseIndex>(std::move(entries));
  std::atomic_store_explicit(&index_, std::move(replacement),
                             std::memory_order_release);
}

std::vector<Candidate> SnapshotStore::Query(std::string_view input,
                                            std::size_t limit) const {
  const auto snapshot =
      std::atomic_load_explicit(&index_, std::memory_order_acquire);
  if (!snapshot || limit == 0) {
    return {};
  }
  return snapshot->Query(input, std::min<std::size_t>(limit, 2));
}

std::size_t SnapshotStore::size() const noexcept {
  const auto snapshot =
      std::atomic_load_explicit(&index_, std::memory_order_acquire);
  return snapshot ? snapshot->size() : 0;
}

}  // namespace yunpin
