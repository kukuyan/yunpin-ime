// SPDX-License-Identifier: Apache-2.0
#include "yunpin/correction_learning.hpp"

#include "yunpin/phrase_engine.hpp"

#include <algorithm>
#include <charconv>
#include <cctype>
#include <limits>
#include <istream>
#include <ostream>
#include <stdexcept>
#include <string_view>
#include <utility>

namespace yunpin {
namespace {

constexpr std::string_view kTsvHeader =
    "date\tentry_id\tphrase\tpinyin\tselections\tcorrected_from\treplacements";
constexpr std::size_t kMaxPhraseCodepoints = 64;
constexpr std::size_t kMaxPhraseBytes = 256;
constexpr std::size_t kMaxEntryIdBytes = 160;
constexpr std::size_t kMaxPinyinLetters = 128;
constexpr std::size_t kMaxLineBytes = 4096;
constexpr std::size_t kMaxHabitStatEntries = 50000;

bool IsLeapYear(int year) noexcept {
  return year % 400 == 0 || (year % 4 == 0 && year % 100 != 0);
}

bool IsDateBucket(std::string_view value) noexcept {
  if (value.size() != 10 || value[4] != '-' || value[7] != '-') {
    return false;
  }
  for (std::size_t i = 0; i < value.size(); ++i) {
    if (i == 4 || i == 7) {
      continue;
    }
    if (value[i] < '0' || value[i] > '9') {
      return false;
    }
  }
  const int year = (value[0] - '0') * 1000 + (value[1] - '0') * 100 +
                   (value[2] - '0') * 10 + (value[3] - '0');
  const int month = (value[5] - '0') * 10 + (value[6] - '0');
  const int day = (value[8] - '0') * 10 + (value[9] - '0');
  if (year < 1970 || month < 1 || month > 12) {
    return false;
  }
  static constexpr int kDaysPerMonth[] = {31, 28, 31, 30, 31, 30,
                                           31, 31, 30, 31, 30, 31};
  const int days =
      month == 2 && IsLeapYear(year) ? 29 : kDaysPerMonth[month - 1];
  return day >= 1 && day <= days;
}

bool HasAsciiControl(std::string_view value) noexcept {
  return std::any_of(value.begin(), value.end(), [](char character) {
    const unsigned char byte = static_cast<unsigned char>(character);
    return byte < 0x20 || byte == 0x7f;
  });
}

bool CountSafeUtf8(std::string_view value, std::size_t* count) noexcept {
  *count = 0;
  for (std::size_t offset = 0; offset < value.size();) {
    const unsigned char first = static_cast<unsigned char>(value[offset]);
    std::uint32_t codepoint = 0;
    std::size_t width = 0;
    if (first < 0x80) {
      codepoint = first;
      width = 1;
    } else if ((first & 0xe0) == 0xc0) {
      codepoint = first & 0x1f;
      width = 2;
    } else if ((first & 0xf0) == 0xe0) {
      codepoint = first & 0x0f;
      width = 3;
    } else if ((first & 0xf8) == 0xf0) {
      codepoint = first & 0x07;
      width = 4;
    } else {
      return false;
    }
    if (offset + width > value.size()) {
      return false;
    }
    for (std::size_t index = 1; index < width; ++index) {
      const unsigned char continuation =
          static_cast<unsigned char>(value[offset + index]);
      if ((continuation & 0xc0) != 0x80) {
        return false;
      }
      codepoint = (codepoint << 6) | (continuation & 0x3f);
    }
    const bool overlong =
        (width == 2 && codepoint < 0x80) ||
        (width == 3 && codepoint < 0x800) ||
        (width == 4 && codepoint < 0x10000);
    const bool invalid_scalar =
        codepoint > 0x10ffff || (codepoint >= 0xd800 && codepoint <= 0xdfff);
    const bool directional_control =
        (codepoint >= 0x202a && codepoint <= 0x202e) ||
        (codepoint >= 0x2066 && codepoint <= 0x2069);
    if (overlong || invalid_scalar || directional_control) {
      return false;
    }
    ++(*count);
    offset += width;
  }
  return true;
}

std::string AsciiLower(std::string_view value) {
  std::string result;
  result.reserve(value.size());
  for (const char character : value) {
    const unsigned char byte = static_cast<unsigned char>(character);
    result.push_back(byte < 0x80
                         ? static_cast<char>(std::tolower(byte))
                         : character);
  }
  return result;
}

bool LooksSensitive(std::string_view value) {
  const std::string lower = AsciiLower(value);
  static constexpr std::string_view kSensitiveMarkers[] = {
      "http://",  "https://", "www.",     "bearer ",
      "password", "passwd",   "token=",   "secret=",
      "api_key",  "apikey",   "-----begin"};
  for (const std::string_view marker : kSensitiveMarkers) {
    if (lower.find(marker) != std::string::npos) {
      return true;
    }
  }
  if (value.find('@') != std::string_view::npos ||
      value.find('/') != std::string_view::npos ||
      value.find('\\') != std::string_view::npos) {
    return true;
  }

  std::size_t digit_run = 0;
  std::size_t dots = 0;
  bool only_address_chars = !value.empty();
  for (const char character : value) {
    if (character >= '0' && character <= '9') {
      ++digit_run;
      if (digit_run >= 7) {
        return true;
      }
    } else {
      digit_run = 0;
      if (character == '.') {
        ++dots;
      } else {
        only_address_chars = false;
      }
    }
  }
  return only_address_chars && dots == 3;
}

bool IsSafeIdentifier(std::string_view value) noexcept {
  return !value.empty() && value.size() <= kMaxEntryIdBytes &&
         !HasAsciiControl(value);
}

std::uint64_t ParseCount(std::string_view value) {
  std::uint64_t result = 0;
  const auto parsed =
      std::from_chars(value.data(), value.data() + value.size(), result);
  if (value.empty() || parsed.ec != std::errc{} ||
      parsed.ptr != value.data() + value.size()) {
    throw std::invalid_argument("invalid habit monitor count");
  }
  return result;
}

void SaturatingIncrement(std::uint64_t* value) noexcept {
  if (*value != std::numeric_limits<std::uint64_t>::max()) {
    ++(*value);
  }
}

std::uint64_t SaturatingAdd(std::uint64_t left,
                            std::uint64_t right) noexcept {
  return left > std::numeric_limits<std::uint64_t>::max() - right
             ? std::numeric_limits<std::uint64_t>::max()
             : left + right;
}

std::vector<std::string> SplitTsv(std::string_view line) {
  std::vector<std::string> fields;
  std::size_t begin = 0;
  while (true) {
    const std::size_t tab = line.find('\t', begin);
    fields.emplace_back(line.substr(begin, tab - begin));
    if (tab == std::string_view::npos) {
      break;
    }
    begin = tab + 1;
  }
  return fields;
}

bool SameEntry(const SelectionEvent& left,
               const SelectionEvent& right) noexcept {
  return left.entry_id == right.entry_id && left.phrase == right.phrase &&
         NormalizePinyin(left.pinyin) == NormalizePinyin(right.pinyin);
}

}  // namespace

std::int64_t HabitStat::net_correction_feedback() const noexcept {
  if (replacement_count >= corrected_from_count) {
    const std::uint64_t difference =
        replacement_count - corrected_from_count;
    return difference >
                   static_cast<std::uint64_t>(
                       std::numeric_limits<std::int64_t>::max())
               ? std::numeric_limits<std::int64_t>::max()
               : static_cast<std::int64_t>(difference);
  }
  const std::uint64_t difference = corrected_from_count - replacement_count;
  return difference >
                 static_cast<std::uint64_t>(
                     std::numeric_limits<std::int64_t>::max())
             ? std::numeric_limits<std::int64_t>::min()
             : -static_cast<std::int64_t>(difference);
}

bool IsMonitorableSelection(const SelectionEvent& event) {
  if (event.context != LearningContext::kNormal || !event.monitorable ||
      !IsDateBucket(event.date_bucket) ||
      !IsSafeIdentifier(event.entry_id) || event.phrase.empty() ||
      event.phrase.size() > kMaxPhraseBytes || HasAsciiControl(event.phrase) ||
      LooksSensitive(event.phrase) || LooksSensitive(event.entry_id)) {
    return false;
  }
  std::size_t codepoints = 0;
  if (!CountSafeUtf8(event.phrase, &codepoints) || codepoints == 0 ||
      codepoints > kMaxPhraseCodepoints) {
    return false;
  }
  const std::string pinyin = NormalizePinyin(event.pinyin);
  return !pinyin.empty() && pinyin.size() <= kMaxPinyinLetters;
}

std::string CorrectionLearning::StatKey(const SelectionEvent& event) {
  return event.date_bucket + '\x1f' + event.entry_id + '\x1f' + event.phrase +
         '\x1f' + NormalizePinyin(event.pinyin);
}

void CorrectionLearning::ClearAdjacencyLocked() {
  last_selection_.reset();
  pending_wrong_.reset();
}

LearningUpdate CorrectionLearning::RecordSelection(SelectionEvent event) {
  std::lock_guard<std::mutex> lock(mutex_);
  LearningUpdate result;
  result.candidate_epoch = candidate_epoch_;
  if (!IsMonitorableSelection(event)) {
    ClearAdjacencyLocked();
    return result;
  }
  event.pinyin = NormalizePinyin(event.pinyin);

  const auto stat_index_for = [&](const SelectionEvent& target)
      -> std::optional<std::size_t> {
    const std::string key = StatKey(target);
    const auto found = stat_indexes_.find(key);
    if (found != stat_indexes_.end()) {
      return found->second;
    }
    if (stats_.size() >= kMaxHabitStatEntries) {
      return std::nullopt;
    }
    const std::size_t index = stats_.size();
    stats_.push_back(HabitStat{target.date_bucket, target.entry_id,
                              target.phrase, target.pinyin});
    stat_indexes_.emplace(key, index);
    return index;
  };

  const std::optional<std::size_t> selected_index = stat_index_for(event);
  if (!selected_index.has_value()) {
    ClearAdjacencyLocked();
    return result;
  }
  SaturatingIncrement(&stats_[*selected_index].selection_count);
  result.recorded = true;

  if (pending_wrong_.has_value() &&
      !SameEntry(*pending_wrong_, event)) {
    const std::optional<std::size_t> wrong_index =
        stat_index_for(*pending_wrong_);
    if (!wrong_index.has_value()) {
      ClearAdjacencyLocked();
      return result;
    }
    SaturatingIncrement(&stats_[*wrong_index].corrected_from_count);
    SaturatingIncrement(&stats_[*selected_index].replacement_count);
    result.correction_completed = true;
    result.requires_requery = true;
    result.feedback = {{pending_wrong_->entry_id, -1}, {event.entry_id, 1}};
    ++candidate_epoch_;
    result.candidate_epoch = candidate_epoch_;
  }

  pending_wrong_.reset();
  last_selection_ = std::move(event);
  return result;
}

bool CorrectionLearning::UndoLastSelection(LearningContext context) {
  std::lock_guard<std::mutex> lock(mutex_);
  if (context != LearningContext::kNormal || !last_selection_.has_value()) {
    ClearAdjacencyLocked();
    return false;
  }
  pending_wrong_ = std::move(last_selection_);
  last_selection_.reset();
  return true;
}

void CorrectionLearning::BreakAdjacency() {
  std::lock_guard<std::mutex> lock(mutex_);
  ClearAdjacencyLocked();
}

std::vector<HabitStat> CorrectionLearning::Query(
    const HabitQuery& query) const {
  std::lock_guard<std::mutex> lock(mutex_);
  return BuildHabitReport(stats_, query);
}

std::vector<HabitStat> BuildHabitReport(const std::vector<HabitStat>& stats,
                                        const HabitQuery& query) {
  if (!query.date_bucket.empty() && !IsDateBucket(query.date_bucket)) {
    return {};
  }
  std::vector<HabitStat> result;
  result.reserve(std::min(query.limit, stats.size()));
  for (const HabitStat& stat : stats) {
    if ((!query.date_bucket.empty() &&
         stat.date_bucket != query.date_bucket) ||
        (query.corrections_only && stat.corrected_from_count == 0 &&
         stat.replacement_count == 0)) {
      continue;
    }
    result.push_back(stat);
  }
  std::sort(result.begin(), result.end(),
            [](const HabitStat& left, const HabitStat& right) {
              if (left.date_bucket != right.date_bucket) {
                return left.date_bucket > right.date_bucket;
              }
              const std::uint64_t left_corrections =
                  SaturatingAdd(left.corrected_from_count,
                                left.replacement_count);
              const std::uint64_t right_corrections =
                  SaturatingAdd(right.corrected_from_count,
                                right.replacement_count);
              if (left_corrections != right_corrections) {
                return left_corrections > right_corrections;
              }
              if (left.selection_count != right.selection_count) {
                return left.selection_count > right.selection_count;
              }
              if (left.phrase != right.phrase) {
                return left.phrase < right.phrase;
              }
              return left.pinyin < right.pinyin;
            });
  if (result.size() > query.limit) {
    result.resize(query.limit);
  }
  return result;
}

std::uint64_t CorrectionLearning::candidate_epoch() const {
  std::lock_guard<std::mutex> lock(mutex_);
  return candidate_epoch_;
}

bool CorrectionLearning::CanReuseCandidateEpoch(
    std::uint64_t cached_epoch) const {
  return cached_epoch == candidate_epoch();
}

void ExportHabitReportTsv(std::ostream& output,
                          const std::vector<HabitStat>& stats) {
  output << kTsvHeader << '\n';
  for (const HabitStat& stat : stats) {
    const SelectionEvent event{stat.date_bucket, stat.entry_id, stat.phrase,
                               stat.pinyin};
    if (!IsMonitorableSelection(event)) {
      continue;
    }
    output << stat.date_bucket << '\t' << stat.entry_id << '\t' << stat.phrase
           << '\t' << NormalizePinyin(stat.pinyin) << '\t'
           << stat.selection_count << '\t' << stat.corrected_from_count << '\t'
           << stat.replacement_count << '\n';
  }
  if (!output) {
    throw std::runtime_error("failed to write habit monitor snapshot");
  }
}

std::vector<HabitStat> ParseHabitReportTsv(std::istream& input,
                                           std::size_t max_rows) {
  std::string line;
  if (!std::getline(input, line) || line != kTsvHeader) {
    throw std::invalid_argument("invalid habit monitor header");
  }

  std::vector<HabitStat> result;
  while (std::getline(input, line)) {
    if (line.empty()) {
      continue;
    }
    if (line.size() > kMaxLineBytes || result.size() >= max_rows) {
      throw std::invalid_argument("habit monitor snapshot exceeds limits");
    }
    const std::vector<std::string> fields = SplitTsv(line);
    if (fields.size() != 7) {
      throw std::invalid_argument("invalid habit monitor row");
    }
    SelectionEvent event{fields[0], fields[1], fields[2], fields[3]};
    if (!IsMonitorableSelection(event)) {
      // Refuse to surface a sensitive or malformed word, and do not include it
      // in an error message.
      continue;
    }
    result.push_back(HabitStat{fields[0], fields[1], fields[2],
                               NormalizePinyin(fields[3]),
                               ParseCount(fields[4]), ParseCount(fields[5]),
                               ParseCount(fields[6])});
  }
  if (!input.eof() && input.fail()) {
    throw std::runtime_error("failed to read habit monitor snapshot");
  }
  return result;
}

}  // namespace yunpin
