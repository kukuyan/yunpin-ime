// SPDX-License-Identifier: Apache-2.0
#include "yunpin/snapshot_store.hpp"

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <fstream>
#include <iomanip>
#include <iostream>
#include <sstream>
#include <string>
#include <utility>
#include <vector>

#if defined(_WIN32)
#define NOMINMAX
#include <windows.h>
#include <psapi.h>
#elif defined(__APPLE__)
#include <mach/mach.h>
#include <sys/resource.h>
#elif defined(__linux__)
#include <sys/resource.h>
#include <unistd.h>
#endif

namespace {

constexpr std::size_t kEntryCount = yunpin::kMaxPrivateSnapshotEntries;
constexpr double kP95BudgetMilliseconds = 20.0;
constexpr double kLoadBudgetMilliseconds = 15000.0;
constexpr std::size_t kWorkingSetBudgetBytes = 256ULL * 1024ULL * 1024ULL;

const std::vector<std::string> kSyllables = {
    "ai",    "bei",  "cheng", "dong", "er",   "fen",  "guo",
    "hua",   "ji",   "kai",   "lin",  "min",  "nan",  "ou",
    "ping",  "qing", "ren",   "shi",  "tian", "wei",  "xi",
    "yang",  "zhong", "an",   "bao",  "cun",  "dao",  "fa",
    "gang",  "he",   "jin",   "li",   "mei",  "ning", "pu",
    "qun",   "rong", "su",    "tong", "wen",  "xin",  "yuan",
    "zhen",  "ao",   "bi",    "chuan", "deng", "fei", "gong",
    "hai",   "jian", "ke",    "long", "mu",   "nuo",  "qi",
    "shan",  "tai",  "wan",   "xiao", "you",  "zhou", "zu",
    "rui"};

std::vector<std::string> SyllablesFor(std::size_t index) {
  const std::size_t radix = kSyllables.size();
  if (index < 10000) {
    return {"zhong", "guo", kSyllables[index % radix],
            kSyllables[(index / radix) % radix]};
  }
  std::vector<std::string> result;
  result.reserve(4);
  for (std::size_t position = 0; position < 4; ++position) {
    result.push_back(kSyllables[index % radix]);
    index /= radix;
  }
  return result;
}

std::string Join(const std::vector<std::string>& syllables,
                 std::size_t count, std::string_view separator) {
  std::string result;
  for (std::size_t index = 0; index < count; ++index) {
    if (index != 0) {
      result += separator;
    }
    result += syllables[index];
  }
  return result;
}

std::size_t CurrentRssBytes() {
#if defined(_WIN32)
  PROCESS_MEMORY_COUNTERS_EX counters{};
  counters.cb = static_cast<DWORD>(sizeof(counters));
  if (GetProcessMemoryInfo(
          GetCurrentProcess(),
          reinterpret_cast<PROCESS_MEMORY_COUNTERS*>(&counters),
          static_cast<DWORD>(sizeof(counters))) == 0) {
    return 0;
  }
  return static_cast<std::size_t>(counters.WorkingSetSize);
#elif defined(__APPLE__)
  mach_task_basic_info_data_t info{};
  mach_msg_type_number_t count = MACH_TASK_BASIC_INFO_COUNT;
  if (task_info(mach_task_self(), MACH_TASK_BASIC_INFO,
                reinterpret_cast<task_info_t>(&info), &count) != KERN_SUCCESS) {
    return 0;
  }
  return static_cast<std::size_t>(info.resident_size);
#elif defined(__linux__)
  std::ifstream statm("/proc/self/statm");
  std::size_t total_pages = 0;
  std::size_t resident_pages = 0;
  if (!(statm >> total_pages >> resident_pages)) {
    return 0;
  }
  const long page_size = sysconf(_SC_PAGESIZE);
  return page_size > 0
             ? resident_pages * static_cast<std::size_t>(page_size)
             : 0;
#else
  return 0;
#endif
}

std::size_t PeakRssBytes() {
#if defined(_WIN32)
  PROCESS_MEMORY_COUNTERS_EX counters{};
  counters.cb = static_cast<DWORD>(sizeof(counters));
  if (GetProcessMemoryInfo(
          GetCurrentProcess(),
          reinterpret_cast<PROCESS_MEMORY_COUNTERS*>(&counters),
          static_cast<DWORD>(sizeof(counters))) == 0) {
    return 0;
  }
  return static_cast<std::size_t>(counters.PeakWorkingSetSize);
#elif defined(__APPLE__) || defined(__linux__)
  rusage usage{};
  if (getrusage(RUSAGE_SELF, &usage) != 0) {
    return 0;
  }
#if defined(__APPLE__)
  return static_cast<std::size_t>(usage.ru_maxrss);
#else
  return static_cast<std::size_t>(usage.ru_maxrss) * 1024ULL;
#endif
#else
  return 0;
#endif
}

double MiB(std::size_t bytes) {
  return static_cast<double>(bytes) / (1024.0 * 1024.0);
}

}  // namespace

int main() {
  const std::size_t baseline_rss = CurrentRssBytes();
  std::vector<std::string> queries;
  queries.reserve(1024);
  // Make a high-collision two-syllable prefix account for more than five per
  // cent of samples, so it is necessarily represented in the measured P95.
  for (std::size_t sample = 0; sample < 64; ++sample) {
    queries.emplace_back("zhongguo");
  }
  yunpin::SnapshotLoadResult parsed;
  std::chrono::steady_clock::time_point parse_start;
  std::chrono::steady_clock::time_point parse_end;
  {
    // A stringstream keeps the fixture synthetic and platform-independent.
    // Its serialized buffer is destroyed immediately after parsing, before
    // the immutable index is built and its steady-state RSS is sampled.
    std::stringstream input;
    input << "phrase\tpinyin\tsource\tuse_count\n";
    for (std::size_t index = 0; index < kEntryCount; ++index) {
      const auto syllables = SyllablesFor(index);
      const std::string spaced = Join(syllables, syllables.size(), " ");
      input << "synthetic-private-" << index << '\t' << spaced
            << "\tsogou_import\t" << (index % 97 + 1) << '\n';
      if (index % 197 == 0) {
        queries.push_back(Join(syllables, 2, ""));
        queries.push_back(Join(syllables, syllables.size(), ""));
      }
    }
    input.seekg(0);
    parse_start = std::chrono::steady_clock::now();
    parsed = yunpin::ParsePrivateSnapshot(input);
    parse_end = std::chrono::steady_clock::now();
  }

  if (!parsed.header_valid || parsed.accepted_rows != kEntryCount ||
      parsed.rejected_rows != 0 || parsed.entries.size() != kEntryCount) {
    std::cerr << "100,000-row private snapshot was not retained intact\n";
    return 1;
  }

  yunpin::SnapshotStore store;
  const auto build_start = std::chrono::steady_clock::now();
  store.Replace(std::move(parsed.entries));
  const auto build_end = std::chrono::steady_clock::now();
  if (store.size() != kEntryCount) {
    std::cerr << "private snapshot index was truncated\n";
    return 1;
  }

  std::size_t sink = 0;
  for (const std::string& query : queries) {
    sink += store.Query(query, 2).size();
  }

  std::vector<double> durations_ms;
  durations_ms.reserve(queries.size());
  for (const std::string& query : queries) {
    const auto start = std::chrono::steady_clock::now();
    sink += store.Query(query, 2).size();
    const auto end = std::chrono::steady_clock::now();
    durations_ms.push_back(
        std::chrono::duration<double, std::milli>(end - start).count());
  }
  std::sort(durations_ms.begin(), durations_ms.end());
  const std::size_t p95_index = static_cast<std::size_t>(
      std::ceil(0.95 * static_cast<double>(durations_ms.size()))) - 1;
  const double p95_ms = durations_ms[p95_index];
  const double max_ms = durations_ms.back();
  const double parse_ms =
      std::chrono::duration<double, std::milli>(parse_end - parse_start)
          .count();
  const double build_ms =
      std::chrono::duration<double, std::milli>(build_end - build_start)
          .count();
  const std::size_t current_rss = CurrentRssBytes();
  const std::size_t peak_rss = PeakRssBytes();
  const std::size_t peak_delta =
      peak_rss > baseline_rss ? peak_rss - baseline_rss : 0;

  std::cout << std::fixed << std::setprecision(3)
            << "private_snapshot_benchmark entries=" << store.size()
            << " queries=" << queries.size() << " parse_ms=" << parse_ms
            << " build_ms=" << build_ms << " p95_ms=" << p95_ms
            << " max_ms=" << max_ms
            << " baseline_rss_mib=" << MiB(baseline_rss)
            << " current_rss_mib=" << MiB(current_rss)
            << " peak_rss_mib=" << MiB(peak_rss)
            << " peak_delta_mib=" << MiB(peak_delta) << " sink=" << sink
            << '\n';

  if (parse_ms + build_ms > kLoadBudgetMilliseconds) {
    std::cerr << "snapshot load budget exceeded: " << parse_ms + build_ms
              << " ms > " << kLoadBudgetMilliseconds << " ms\n";
    return 1;
  }
  if (p95_ms > kP95BudgetMilliseconds) {
    std::cerr << "P95 budget exceeded: " << p95_ms << " ms > "
              << kP95BudgetMilliseconds << " ms\n";
    return 1;
  }
  if (peak_rss != 0 && peak_delta > kWorkingSetBudgetBytes) {
    std::cerr << "working-set budget exceeded: " << MiB(peak_delta)
              << " MiB > " << MiB(kWorkingSetBudgetBytes) << " MiB\n";
    return 1;
  }
  return 0;
}
