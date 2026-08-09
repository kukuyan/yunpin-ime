// SPDX-License-Identifier: Apache-2.0
#include "yunpin/phrase_engine.hpp"

#include <algorithm>
#include <cctype>
#include <functional>
#include <sstream>
#include <stdexcept>
#include <unordered_map>
#include <unordered_set>
#include <utility>

namespace yunpin {
namespace {

constexpr std::size_t kHardMaxFuzzyAliases = 64;
constexpr std::int32_t kMaxCorrectionScore = 1000000;
static_assert(std::atomic_bool::is_always_lock_free,
              "YunPin requires lock-free tombstone reads");
static_assert(std::atomic<std::int32_t>::is_always_lock_free,
              "YunPin requires lock-free correction score reads");
static_assert(std::atomic<std::uint32_t>::is_always_lock_free,
              "YunPin requires lock-free candidate revision reads");

std::vector<std::string> BuiltInSyllables() {
  // Standard tone-free Hanyu Pinyin spellings, using v for keyboard ü. Keeping
  // a reviewed finite set prevents arbitrary substring rewrites from turning
  // fuzzy input into an unbounded candidate source.
  static constexpr const char* kSyllables = R"(
    a ai an ang ao
    ba bai ban bang bao bei ben beng bi bian biao bie bin bing bo bu
    ca cai can cang cao ce cen ceng cha chai chan chang chao che chen cheng
    chi chong chou chu chua chuai chuan chuang chui chun chuo ci cong cou cu
    cuan cui cun cuo
    da dai dan dang dao de dei den deng di dia dian diao die ding diu dong dou
    du duan dui dun duo
    e ei en eng er
    fa fan fang fei fen feng fo fou fu
    ga gai gan gang gao ge gei gen geng gong gou gu gua guai guan guang gui
    gun guo
    ha hai han hang hao he hei hen heng hong hou hu hua huai huan huang hui hun
    huo
    ji jia jian jiang jiao jie jin jing jiong jiu ju juan jue jun
    ka kai kan kang kao ke ken keng kong kou ku kua kuai kuan kuang kui kun kuo
    la lai lan lang lao le lei leng li lia lian liang liao lie lin ling liu lo
    long lou lu luan lue lun luo lv lvan lve
    ma mai man mang mao me mei men meng mi mian miao mie min ming miu mo mou mu
    na nai nan nang nao ne nei nen neng ni nian niang niao nie nin ning niu
    nong nou nu nuan nue nuo nv nve
    o ou
    pa pai pan pang pao pei pen peng pi pian piao pie pin ping po pou pu
    qi qia qian qiang qiao qie qin qing qiong qiu qu quan que qun
    ran rang rao re ren reng ri rong rou ru rua ruan rui run ruo
    sa sai san sang sao se sen seng sha shai shan shang shao she shei shen
    sheng shi shou shu shua shuai shuan shuang shui shun shuo si song sou su
    suan sui sun suo
    ta tai tan tang tao te teng ti tian tiao tie ting tong tou tu tuan tui tun
    tuo
    wa wai wan wang wei wen weng wo wu
    xi xia xian xiang xiao xie xin xing xiong xiu xu xuan xue xun
    ya yan yang yao ye yi yin ying yo yong you yu yuan yue yun
    za zai zan zang zao ze zei zen zeng zha zhai zhan zhang zhao zhe zhei zhen
    zheng zhi zhong zhou zhu zhua zhuai zhuan zhuang zhui zhun zhuo zi zong
    zou zu zuan zui zun zuo
  )";

  std::istringstream input(kSyllables);
  std::vector<std::string> result;
  for (std::string syllable; input >> syllable;) {
    result.push_back(std::move(syllable));
  }
  std::sort(result.begin(), result.end());
  result.erase(std::unique(result.begin(), result.end()), result.end());
  return result;
}

bool IsHardBoundary(unsigned char value) noexcept {
  return std::isspace(value) != 0 || value == '\'' || value == '-';
}

bool StartsWith(std::string_view value, std::string_view prefix) noexcept {
  return value.size() >= prefix.size() &&
         value.compare(0, prefix.size(), prefix) == 0;
}

bool EndsWith(std::string_view value, std::string_view suffix) noexcept {
  return value.size() >= suffix.size() &&
         value.compare(value.size() - suffix.size(), suffix.size(), suffix) ==
             0;
}

bool IsPersonalOrigin(PhraseOrigin origin) noexcept {
  return origin == PhraseOrigin::kPersonal ||
         origin == PhraseOrigin::kHistory ||
         origin == PhraseOrigin::kImported;
}

int OriginTier(const Candidate& candidate) noexcept {
  if (candidate.pinned && candidate.is_personal()) {
    return 3;
  }
  if (candidate.is_personal()) {
    return 2;
  }
  if (candidate.origin == PhraseOrigin::kPublic) {
    return 1;
  }
  return 0;
}

int MatchTier(MatchKind match) noexcept {
  switch (match) {
    case MatchKind::kExactFull:
      return 2;
    case MatchKind::kFullPrefix:
      return 1;
    case MatchKind::kInitials:
      return 0;
  }
  return 0;
}

std::string PrefixUpperBound(std::string_view prefix) {
  // Normalized pinyin contains only a-z. '{' sorts immediately after 'z'.
  std::string result(prefix);
  result.push_back('{');
  return result;
}

std::size_t CompleteSyllablePrefixCount(const PhraseEntry& entry,
                                        std::string_view query) {
  std::size_t count = 0;
  std::size_t offset = 0;
  for (const std::string& syllable : entry.syllables) {
    if (query.size() < offset + syllable.size() ||
        query.compare(offset, syllable.size(), syllable) != 0) {
      break;
    }
    offset += syllable.size();
    ++count;
  }
  return count;
}

template <typename Index>
auto PrefixRange(const Index& index, std::string_view prefix) {
  const auto begin = std::lower_bound(
      index.begin(), index.end(), prefix,
      [](const auto& key_ref, std::string_view value) {
        return key_ref.key < value;
      });
  const std::string upper = PrefixUpperBound(prefix);
  const auto end = std::lower_bound(
      begin, index.end(), std::string_view(upper),
      [](const auto& key_ref, std::string_view value) {
        return key_ref.key < value;
      });
  return std::make_pair(begin, end);
}

void AppendValidVariant(std::string candidate,
                        const PinyinSegmenter& segmenter,
                        std::vector<std::string>* variants,
                        std::unordered_set<std::string>* seen,
                        std::size_t limit) {
  if (variants->size() >= limit || !segmenter.IsSyllable(candidate) ||
      !seen->insert(candidate).second) {
    return;
  }
  variants->push_back(std::move(candidate));
}

void ExpandInitialPair(std::string_view long_initial,
                       std::string_view short_initial,
                       const PinyinSegmenter& segmenter,
                       std::vector<std::string>* variants,
                       std::unordered_set<std::string>* seen,
                       std::size_t limit) {
  const std::vector<std::string> snapshot = *variants;
  for (const std::string& value : snapshot) {
    if (StartsWith(value, long_initial)) {
      AppendValidVariant(
          std::string(short_initial) + value.substr(long_initial.size()),
          segmenter, variants, seen, limit);
    } else if (StartsWith(value, short_initial)) {
      AppendValidVariant(
          std::string(long_initial) + value.substr(short_initial.size()),
          segmenter, variants, seen, limit);
    }
  }
}

void ExpandFinalPair(std::string_view long_final,
                     std::string_view short_final,
                     const PinyinSegmenter& segmenter,
                     std::vector<std::string>* variants,
                     std::unordered_set<std::string>* seen,
                     std::size_t limit) {
  const std::vector<std::string> snapshot = *variants;
  for (const std::string& value : snapshot) {
    if (EndsWith(value, long_final)) {
      AppendValidVariant(
          value.substr(0, value.size() - long_final.size()) +
              std::string(short_final),
          segmenter, variants, seen, limit);
    } else if (EndsWith(value, short_final)) {
      AppendValidVariant(
          value.substr(0, value.size() - short_final.size()) +
              std::string(long_final),
          segmenter, variants, seen, limit);
    }
  }
}

std::vector<std::string> FuzzySyllableVariants(
    const std::string& syllable, const FuzzyConfig& config,
    const PinyinSegmenter& segmenter, std::size_t limit) {
  std::vector<std::string> variants{syllable};
  std::unordered_set<std::string> seen{syllable};
  if (config.zh_z) {
    ExpandInitialPair("zh", "z", segmenter, &variants, &seen, limit);
  }
  if (config.ch_c) {
    ExpandInitialPair("ch", "c", segmenter, &variants, &seen, limit);
  }
  if (config.sh_s) {
    ExpandInitialPair("sh", "s", segmenter, &variants, &seen, limit);
  }
  if (config.n_l) {
    ExpandInitialPair("n", "l", segmenter, &variants, &seen, limit);
  }
  if (config.en_eng) {
    ExpandFinalPair("eng", "en", segmenter, &variants, &seen, limit);
  }
  if (config.in_ing) {
    ExpandFinalPair("ing", "in", segmenter, &variants, &seen, limit);
  }
  return variants;
}

}  // namespace

bool Candidate::is_personal() const noexcept {
  return IsPersonalOrigin(origin);
}

std::string NormalizePinyin(std::string_view input) {
  std::string result;
  result.reserve(input.size());

  for (std::size_t i = 0; i < input.size(); ++i) {
    const unsigned char raw = static_cast<unsigned char>(input[i]);
    if (std::isspace(raw) != 0 || raw == '\'' || raw == '-') {
      continue;
    }
    if (raw >= '1' && raw <= '5') {
      continue;
    }
    const char lower = static_cast<char>(std::tolower(raw));
    if (lower < 'a' || lower > 'z') {
      return {};
    }
    if (lower == 'u' && i + 1 < input.size() && input[i + 1] == ':') {
      result.push_back('v');
      ++i;
      continue;
    }
    result.push_back(lower);
  }
  return result;
}

std::vector<std::string> SplitPinyin(std::string_view input) {
  std::vector<std::string> result;
  std::string token;
  token.reserve(input.size());

  const auto flush = [&]() {
    if (!token.empty()) {
      result.push_back(token);
      token.clear();
    }
  };

  for (std::size_t i = 0; i < input.size(); ++i) {
    const unsigned char raw = static_cast<unsigned char>(input[i]);
    if (std::isspace(raw) != 0 || raw == '\'' || raw == '-') {
      flush();
      continue;
    }
    if (raw >= '1' && raw <= '5') {
      continue;
    }
    const char lower = static_cast<char>(std::tolower(raw));
    if (lower < 'a' || lower > 'z') {
      return {};
    }
    if (lower == 'u' && i + 1 < input.size() && input[i + 1] == ':') {
      token.push_back('v');
      ++i;
      continue;
    }
    token.push_back(lower);
  }
  flush();
  return result;
}

PinyinSegmenter::PinyinSegmenter(SegmenterLimits limits)
    : limits_(limits), syllables_(BuiltInSyllables()) {
  for (const std::string& syllable : syllables_) {
    max_syllable_length_ = std::max(max_syllable_length_, syllable.size());
  }
}

bool PinyinSegmenter::IsSyllable(std::string_view syllable) const {
  const std::string normalized = NormalizePinyin(syllable);
  return !normalized.empty() &&
         std::binary_search(syllables_.begin(), syllables_.end(), normalized);
}

std::vector<std::vector<std::string>> PinyinSegmenter::SegmentChunk(
    std::string_view chunk, bool explicit_boundary) const {
  const std::string normalized = NormalizePinyin(chunk);
  if (normalized.empty() || normalized.size() > limits_.max_input_letters ||
      limits_.max_paths == 0 || limits_.max_syllables_per_path == 0) {
    return {};
  }

  if (explicit_boundary && IsSyllable(normalized)) {
    return {{normalized}};
  }

  std::vector<std::vector<std::string>> paths;
  std::vector<std::string> current;
  std::function<void(std::size_t)> visit = [&](std::size_t offset) {
    if (paths.size() >= limits_.max_paths) {
      return;
    }
    if (offset == normalized.size()) {
      paths.push_back(current);
      return;
    }
    if (current.size() >= limits_.max_syllables_per_path) {
      return;
    }

    const std::size_t remaining = normalized.size() - offset;
    const std::size_t max_length =
        std::min(max_syllable_length_, remaining);
    for (std::size_t length = max_length; length > 0; --length) {
      const std::string_view candidate(normalized.data() + offset, length);
      if (!std::binary_search(syllables_.begin(), syllables_.end(),
                              candidate)) {
        continue;
      }
      current.emplace_back(candidate);
      visit(offset + length);
      current.pop_back();
      if (paths.size() >= limits_.max_paths) {
        return;
      }
    }
  };
  visit(0);
  return paths;
}

std::vector<std::vector<std::string>> PinyinSegmenter::Segment(
    std::string_view input) const {
  const std::string normalized = NormalizePinyin(input);
  if (normalized.empty() || normalized.size() > limits_.max_input_letters ||
      limits_.max_paths == 0 || limits_.max_syllables_per_path == 0) {
    return {};
  }

  std::vector<std::string> raw_chunks;
  std::string current;
  bool saw_boundary = false;
  for (const char value : input) {
    const unsigned char raw = static_cast<unsigned char>(value);
    if (IsHardBoundary(raw)) {
      saw_boundary = true;
      if (!current.empty()) {
        raw_chunks.push_back(std::move(current));
        current.clear();
      }
      continue;
    }
    current.push_back(value);
  }
  if (!current.empty()) {
    raw_chunks.push_back(std::move(current));
  }
  if (raw_chunks.empty()) {
    return {};
  }

  std::vector<std::vector<std::string>> combined(1);
  for (const std::string& raw_chunk : raw_chunks) {
    const auto chunk_paths = SegmentChunk(raw_chunk, saw_boundary);
    if (chunk_paths.empty()) {
      return {};
    }

    std::vector<std::vector<std::string>> next;
    next.reserve(std::min(
        limits_.max_paths, combined.size() * chunk_paths.size()));
    for (const auto& prefix : combined) {
      for (const auto& suffix : chunk_paths) {
        if (prefix.size() + suffix.size() >
            limits_.max_syllables_per_path) {
          continue;
        }
        std::vector<std::string> path = prefix;
        path.insert(path.end(), suffix.begin(), suffix.end());
        next.push_back(std::move(path));
        if (next.size() >= limits_.max_paths) {
          break;
        }
      }
      if (next.size() >= limits_.max_paths) {
        break;
      }
    }
    combined = std::move(next);
    if (combined.empty()) {
      return {};
    }
  }
  return combined;
}

bool FuzzyConfig::enabled() const noexcept {
  return max_aliases > 1 &&
         (zh_z || ch_c || sh_s || n_l || en_eng || in_ing);
}

FuzzyConfig FuzzyConfig::Common() {
  return FuzzyConfig{/*zh_z=*/true,
                     /*ch_c=*/true,
                     /*sh_s=*/true,
                     /*n_l=*/true,
                     /*en_eng=*/true,
                     /*in_ing=*/true,
                     /*max_aliases=*/16};
}

std::vector<std::string> ExpandFuzzyAliases(
    std::string_view input, const FuzzyConfig& config,
    const PinyinSegmenter& segmenter) {
  const std::string literal = NormalizePinyin(input);
  if (literal.empty()) {
    return {};
  }

  const std::size_t limit = std::min(
      kHardMaxFuzzyAliases, std::max<std::size_t>(1, config.max_aliases));
  std::vector<std::string> aliases{literal};
  if (literal.size() <= 2 || !config.enabled() || limit <= 1) {
    return aliases;
  }

  const auto paths = segmenter.Segment(input);
  std::unordered_set<std::string> seen{literal};
  for (const auto& path : paths) {
    std::vector<std::string> partial{std::string()};
    for (const std::string& syllable : path) {
      const auto variants =
          FuzzySyllableVariants(syllable, config, segmenter, limit);
      std::vector<std::string> next;
      next.reserve(std::min(limit, partial.size() * variants.size()));
      for (const std::string& prefix : partial) {
        for (const std::string& variant : variants) {
          next.push_back(prefix + variant);
          if (next.size() >= limit) {
            break;
          }
        }
        if (next.size() >= limit) {
          break;
        }
      }
      partial = std::move(next);
      if (partial.empty()) {
        break;
      }
    }

    for (std::string& alias : partial) {
      if (seen.insert(alias).second) {
        aliases.push_back(std::move(alias));
        if (aliases.size() >= limit) {
          return aliases;
        }
      }
    }
  }
  return aliases;
}

std::vector<std::string> ExpandFuzzyAliases(
    std::string_view input, const FuzzyConfig& config) {
  static const PinyinSegmenter segmenter;
  return ExpandFuzzyAliases(input, config, segmenter);
}

PhraseIndex::PhraseIndex(std::vector<PhraseEntry> entries)
    : PhraseIndex(std::move(entries), FuzzyConfig{}) {}

PhraseIndex::PhraseIndex(std::vector<PhraseEntry> entries,
                         FuzzyConfig fuzzy_config)
    : fuzzy_config_(fuzzy_config) {
  entries_.reserve(entries.size());
  full_index_.reserve(entries.size());
  initials_index_.reserve(entries.size());

  std::unordered_set<std::string> ids;
  ids.reserve(entries.size());

  for (PhraseEntry& entry : entries) {
    if (entry.id.empty() || entry.text.empty() || entry.syllables.empty()) {
      throw std::invalid_argument("phrase entries require id, text and pinyin");
    }
    if (!ids.insert(entry.id).second) {
      throw std::invalid_argument("duplicate phrase id: " + entry.id);
    }

    std::string full;
    std::string initials;
    std::vector<std::string> normalized_syllables;
    normalized_syllables.reserve(entry.syllables.size());
    for (const std::string& raw_syllable : entry.syllables) {
      const std::string syllable = NormalizePinyin(raw_syllable);
      if (syllable.empty()) {
        throw std::invalid_argument("invalid pinyin in phrase id: " + entry.id);
      }
      normalized_syllables.push_back(syllable);
      full += syllable;
      initials.push_back(syllable.front());
    }
    entry.syllables = std::move(normalized_syllables);

    const std::size_t index = entries_.size();
    entries_.push_back(
        IndexedEntry{std::move(entry), std::move(full), std::move(initials)});
    full_index_.push_back(KeyRef{entries_.back().full_pinyin, index});
    initials_index_.push_back(KeyRef{entries_.back().initials, index});
  }

  const auto by_key_then_index = [](const KeyRef& left, const KeyRef& right) {
    if (left.key != right.key) {
      return left.key < right.key;
    }
    return left.entry_index < right.entry_index;
  };
  std::sort(full_index_.begin(), full_index_.end(), by_key_then_index);
  std::sort(initials_index_.begin(), initials_index_.end(), by_key_then_index);

  if (!entries_.empty()) {
    tombstones_ = std::make_unique<std::atomic_bool[]>(entries_.size());
    correction_scores_ =
        std::make_unique<std::atomic<std::int32_t>[]>(entries_.size());
    for (std::size_t index = 0; index < entries_.size(); ++index) {
      tombstones_[index].store(entries_[index].entry.tombstoned,
                               std::memory_order_relaxed);
      correction_scores_[index].store(
          std::clamp(entries_[index].entry.correction_score,
                     -kMaxCorrectionScore, kMaxCorrectionScore),
          std::memory_order_relaxed);
    }
  }
}

std::vector<Candidate> PhraseIndex::Query(std::string_view input,
                                          std::size_t limit) const {
  if (limit == 0) {
    return {};
  }
  const std::string query = NormalizePinyin(input);
  if (query.empty()) {
    return {};
  }

  struct MatchRecord {
    MatchKind kind;
    std::string alias;
  };
  std::unordered_map<std::size_t, MatchRecord> matches;

  std::vector<std::string> aliases;
  if (fuzzy_config_.enabled()) {
    aliases = ExpandFuzzyAliases(input, fuzzy_config_);
  } else {
    aliases.push_back(query);
  }

  for (std::size_t alias_index = 0; alias_index < aliases.size();
       ++alias_index) {
    const std::string& alias = aliases[alias_index];
    const auto [full_begin, full_end] = PrefixRange(full_index_, alias);
    matches.reserve(matches.size() +
                    static_cast<std::size_t>(full_end - full_begin));
    for (auto it = full_begin; it != full_end; ++it) {
      // Only a literal exact spelling receives the exact-intent boost. A fuzzy
      // alias that reaches a complete code remains a prefix-quality match, so
      // it cannot displace a literal homophone.
      const MatchKind kind = alias_index == 0 && it->key == query
                                 ? MatchKind::kExactFull
                                 : MatchKind::kFullPrefix;
      auto [position, inserted] = matches.emplace(
          it->entry_index, MatchRecord{kind, alias});
      if (!inserted && MatchTier(kind) > MatchTier(position->second.kind)) {
        position->second = MatchRecord{kind, alias};
      }
    }
  }

  // Two initials are useful for ordinary phrases. Pinned long phrases have the
  // stricter four-initial recall threshold enforced below.
  if (query.size() >= 2) {
    const auto [initials_begin, initials_end] =
        PrefixRange(initials_index_, query);
    matches.reserve(matches.size() +
                    static_cast<std::size_t>(initials_end - initials_begin));
    for (auto it = initials_begin; it != initials_end; ++it) {
      matches.emplace(it->entry_index,
                      MatchRecord{MatchKind::kInitials, query});
    }
  }

  std::vector<Candidate> ranked;
  ranked.reserve(matches.size());
  for (const auto& [index, match] : matches) {
    const IndexedEntry& indexed = entries_[index];
    const PhraseEntry& entry = indexed.entry;
    if (tombstones_[index].load(std::memory_order_relaxed) ||
        (entry.learned && !entry.pinned && entry.use_count < 2)) {
      continue;
    }

    const bool pinned_personal = entry.pinned && IsPersonalOrigin(entry.origin);
    if (IsPersonalOrigin(entry.origin) && entry.syllables.size() >= 2 &&
        query.size() < 2) {
      // A single letter is not yet a reliable syllable or initials intent.
      // Never let a private/history/import overlay turn `h` into a learned
      // multi-syllable phrase; upstream single-character completion remains
      // available to the native Rime translator.
      continue;
    }
    const bool pinned_long =
        pinned_personal &&
        entry.syllables.size() >= kLongPhraseMinSyllables;
    // A one-syllable prefix such as `he` is useful for one- and two-syllable
    // words, but is far too broad for injecting an arbitrary three-syllable
    // (or longer) phrase such as `he bing wei`. Non-pinned long entries must
    // therefore demonstrate two complete full-pinyin syllables. Initial-only
    // recall is deliberately unavailable for them; manually pinned long
    // phrases keep the separately documented four-initial escape hatch below.
    const bool non_pinned_long =
        !pinned_personal && entry.syllables.size() >= 3;
    if (non_pinned_long && match.kind == MatchKind::kFullPrefix &&
        CompleteSyllablePrefixCount(entry, match.alias) < 2) {
      continue;
    }
    if (non_pinned_long && match.kind == MatchKind::kInitials) {
      continue;
    }
    if (pinned_long && match.kind == MatchKind::kFullPrefix &&
        CompleteSyllablePrefixCount(entry, match.alias) < 2) {
      continue;
    }
    if (pinned_long && match.kind == MatchKind::kInitials &&
        query.size() < 4) {
      continue;
    }

    ranked.push_back(Candidate{entry.id,
                               entry.text,
                               indexed.full_pinyin,
                               indexed.initials,
                               entry.origin,
                               match.kind,
                               entry.use_count,
                               entry.static_weight,
                               entry.pinned,
                               correction_scores_[index].load(
                                   std::memory_order_relaxed)});
  }

  std::sort(ranked.begin(), ranked.end(), [](const Candidate& left,
                                              const Candidate& right) {
    // An exact full-pinyin intent is decisive. The target exact phrase is still
    // ordered by the documented source tiers when homophones share a code.
    if (left.match == MatchKind::kExactFull ||
        right.match == MatchKind::kExactFull) {
      if (left.match != right.match) {
        return left.match == MatchKind::kExactFull;
      }
    }
    if (OriginTier(left) != OriginTier(right)) {
      return OriginTier(left) > OriginTier(right);
    }
    if (MatchTier(left.match) != MatchTier(right.match)) {
      return MatchTier(left.match) > MatchTier(right.match);
    }
    if (left.correction_score != right.correction_score) {
      return left.correction_score > right.correction_score;
    }
    if (left.use_count != right.use_count) {
      return left.use_count > right.use_count;
    }
    if (left.static_weight != right.static_weight) {
      return left.static_weight > right.static_weight;
    }
    if (left.full_pinyin != right.full_pinyin) {
      return left.full_pinyin < right.full_pinyin;
    }
    if (left.text != right.text) {
      return left.text < right.text;
    }
    return left.id < right.id;
  });

  // Keep the on-screen quota consistent with menu/page_size defaults.
  constexpr std::size_t kFirstPageCandidateCount = 8;
  const std::size_t first_page_target =
      std::min<std::size_t>(kFirstPageCandidateCount, limit);
  std::vector<Candidate> result;
  result.reserve(std::min(limit, ranked.size()));
  std::vector<bool> selected(ranked.size(), false);
  std::size_t personal_count = 0;

  for (std::size_t i = 0;
       i < ranked.size() && result.size() < first_page_target; ++i) {
    if (ranked[i].is_personal() && personal_count >= 2) {
      continue;
    }
    if (ranked[i].is_personal()) {
      ++personal_count;
    }
    result.push_back(ranked[i]);
    selected[i] = true;
  }

  // Never put a third personal candidate into a partially-filled first page.
  if (result.size() < first_page_target) {
    return result;
  }

  for (std::size_t i = 0; i < ranked.size() && result.size() < limit; ++i) {
    if (!selected[i]) {
      result.push_back(ranked[i]);
    }
  }
  return result;
}

bool PhraseIndex::ApplyTombstone(std::string_view id) {
  for (std::size_t index = 0; index < entries_.size(); ++index) {
    if (entries_[index].entry.id == id) {
      if (!tombstones_[index].exchange(true, std::memory_order_acq_rel)) {
        revision_.fetch_add(1, std::memory_order_release);
      }
      return true;
    }
  }
  return false;
}

bool PhraseIndex::ApplyCorrectionFeedback(std::string_view id,
                                          std::int32_t delta) {
  for (std::size_t index = 0; index < entries_.size(); ++index) {
    if (entries_[index].entry.id != id) {
      continue;
    }
    if (delta == 0) {
      return true;
    }

    auto& score = correction_scores_[index];
    std::int32_t current = score.load(std::memory_order_relaxed);
    while (true) {
      const std::int32_t next =
          delta > 0
              ? (current > kMaxCorrectionScore -
                               std::min(delta, kMaxCorrectionScore)
                     ? kMaxCorrectionScore
                     : current + std::min(delta, kMaxCorrectionScore))
              : (current < -kMaxCorrectionScore -
                               std::max(delta, -kMaxCorrectionScore)
                     ? -kMaxCorrectionScore
                     : current + std::max(delta, -kMaxCorrectionScore));
      if (next == current) {
        return true;
      }
      if (score.compare_exchange_weak(current, next,
                                      std::memory_order_acq_rel,
                                      std::memory_order_relaxed)) {
        revision_.fetch_add(1, std::memory_order_release);
        return true;
      }
    }
  }
  return false;
}

std::uint32_t PhraseIndex::revision() const noexcept {
  return revision_.load(std::memory_order_acquire);
}

bool PhraseIndex::CanReuseRevision(
    std::uint32_t cached_revision) const noexcept {
  return cached_revision == revision();
}

std::size_t PhraseIndex::size() const noexcept {
  return entries_.size();
}

}  // namespace yunpin
