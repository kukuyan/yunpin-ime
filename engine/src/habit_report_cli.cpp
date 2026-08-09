// SPDX-License-Identifier: Apache-2.0
#include "yunpin/correction_learning.hpp"

#include <charconv>
#include <cstddef>
#include <iostream>
#include <stdexcept>
#include <string>
#include <string_view>

namespace {

void Usage(std::ostream& output) {
  output
      << "usage: yunpin_habit_report [--date YYYY-MM-DD] "
         "[--corrections-only] [--limit N]\n"
      << "Reads an explicitly requested local habit-report stream from stdin; "
         "it never opens a database, file, or network connection.\n";
}

std::size_t ParseLimit(std::string_view value) {
  std::size_t result = 0;
  const auto parsed =
      std::from_chars(value.data(), value.data() + value.size(), result);
  if (value.empty() || parsed.ec != std::errc{} ||
      parsed.ptr != value.data() + value.size() || result > 50000) {
    throw std::invalid_argument("invalid --limit");
  }
  return result;
}

}  // namespace

int main(int argc, char** argv) {
  try {
    yunpin::HabitQuery query;
    for (int index = 1; index < argc; ++index) {
      const std::string_view argument(argv[index]);
      if (argument == "--help") {
        Usage(std::cout);
        return 0;
      }
      if (argument == "--corrections-only") {
        query.corrections_only = true;
        continue;
      }
      if (argument == "--date" && index + 1 < argc) {
        query.date_bucket = argv[++index];
        continue;
      }
      if (argument == "--limit" && index + 1 < argc) {
        query.limit = ParseLimit(argv[++index]);
        continue;
      }
      Usage(std::cerr);
      return 2;
    }

    const auto parsed = yunpin::ParseHabitReportTsv(std::cin);
    const auto report = yunpin::BuildHabitReport(parsed, query);
    std::cout << "date\tphrase\tpinyin\tselections\tcorrected_from\t"
                 "replacements\tnet_feedback\n";
    for (const auto& stat : report) {
      std::cout << stat.date_bucket << '\t' << stat.phrase << '\t'
                << stat.pinyin << '\t' << stat.selection_count << '\t'
                << stat.corrected_from_count << '\t'
                << stat.replacement_count << '\t'
                << stat.net_correction_feedback() << '\n';
    }
    return 0;
  } catch (const std::exception&) {
    // Never echo a malformed row: it could be precisely the sensitive value
    // that the report boundary is meant to suppress.
    std::cerr << "yunpin_habit_report: invalid local report input\n";
    return 1;
  }
}
