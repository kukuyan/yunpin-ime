// SPDX-License-Identifier: Apache-2.0
#include "yunpin/phrase_engine.hpp"

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstdint>
#include <iomanip>
#include <iostream>
#include <string>
#include <utility>
#include <vector>

namespace {

constexpr std::size_t kEntryCount = 50000;
constexpr double kP95BudgetMilliseconds = 20.0;

const std::vector<std::string> kSyllables = {
    "ai",    "bei",  "cheng", "dong", "er",    "fen",  "guo",
    "hua",   "ji",   "kai",   "lin",  "min",   "nan",  "ou",
    "ping",  "qing", "ren",   "shi",  "tian",  "wei",  "xi",
    "yang",  "zhong", "an",   "bao",  "cun",   "dao",  "fa",
    "gang",  "he",   "jin",   "li",   "mei",   "ning", "pu",
    "qun",   "rong", "su",    "tong", "wen",   "xin",  "yuan",
    "zhen",  "ao",   "bi",    "chuan", "deng", "fei",  "gong",
    "hai",   "jian", "ke",    "long", "mu",    "nuo",  "qi",
    "shan",  "tai",  "wan",   "xiao", "you",   "zhou", "zu",
    "rui"};

std::vector<std::string> SyllablesFor(std::size_t index) {
  const std::size_t size = kSyllables.size();
  return {kSyllables[index % size],
          kSyllables[(index / size + index * 7) % size],
          kSyllables[(index / (size * size) + index * 13) % size],
          kSyllables[(index * 29 + 11) % size],
          kSyllables[(index * 37 + 17) % size]};
}

std::string JoinPrefix(const std::vector<std::string>& syllables,
                       std::size_t count) {
  std::string result;
  for (std::size_t i = 0; i < count; ++i) {
    result += syllables[i];
  }
  return result;
}

std::string Initials(const std::vector<std::string>& syllables) {
  std::string result;
  for (const auto& syllable : syllables) {
    result.push_back(syllable.front());
  }
  return result;
}

}  // namespace

int main() {
  std::vector<yunpin::PhraseEntry> entries;
  entries.reserve(kEntryCount);
  std::vector<std::string> queries;
  queries.reserve(320);

  for (std::size_t i = 0; i < kEntryCount; ++i) {
    auto syllables = SyllablesFor(i);
    if (i < 160) {
      queries.push_back(JoinPrefix(syllables, 2));
      queries.push_back(Initials(syllables).substr(0, 4));
    }
    entries.push_back(yunpin::PhraseEntry{
        "synthetic-" + std::to_string(i),
        "synthetic phrase " + std::to_string(i),
        std::move(syllables),
        i % 9 == 0 ? yunpin::PhraseOrigin::kPersonal
                   : (i % 3 == 0 ? yunpin::PhraseOrigin::kPublic
                                 : yunpin::PhraseOrigin::kBase),
        static_cast<std::uint64_t>(i % 97),
        static_cast<std::int64_t>(i % 1009),
        i % 997 == 0,
        false,
        false});
  }

  const auto build_start = std::chrono::steady_clock::now();
  const yunpin::PhraseIndex index(std::move(entries));
  const auto build_end = std::chrono::steady_clock::now();

  std::size_t sink = 0;
  for (const std::string& query : queries) {
    sink += index.Query(query, 9).size();
  }

  std::vector<double> durations_ms;
  durations_ms.reserve(queries.size());
  for (const std::string& query : queries) {
    const auto start = std::chrono::steady_clock::now();
    sink += index.Query(query, 9).size();
    const auto end = std::chrono::steady_clock::now();
    durations_ms.push_back(
        std::chrono::duration<double, std::milli>(end - start).count());
  }

  std::sort(durations_ms.begin(), durations_ms.end());
  const std::size_t p95_index = static_cast<std::size_t>(
      std::ceil(0.95 * static_cast<double>(durations_ms.size()))) - 1;
  const double p95_ms = durations_ms[p95_index];
  const double max_ms = durations_ms.back();
  const double build_ms =
      std::chrono::duration<double, std::milli>(build_end - build_start)
          .count();

  std::cout << std::fixed << std::setprecision(3)
            << "phrase_engine_benchmark entries=" << index.size()
            << " queries=" << queries.size() << " build_ms=" << build_ms
            << " p95_ms=" << p95_ms << " max_ms=" << max_ms
            << " sink=" << sink << '\n';

  if (p95_ms > kP95BudgetMilliseconds) {
    std::cerr << "P95 budget exceeded: " << p95_ms << " ms > "
              << kP95BudgetMilliseconds << " ms\n";
    return 1;
  }
  return 0;
}
