// SPDX-License-Identifier: Apache-2.0
//
// Exercises the real YunPinFilter against the stub librime headers in
// rime_stubs/. The filter, the snapshot store and the phrase engine are the
// production sources; only librime is replaced. This keeps the candidate
// ordering rules testable on a machine that cannot build librime.
//
// The rule these cases exist to protect: the expression actions must never
// occupy a head slot. The first candidate is what the space bar commits and
// 1/2 are the most used selection keys, so an action there makes the input
// method open a browser instead of typing.

// Assertions must survive a Release configuration.
#undef NDEBUG

#include "rime_yunpin_filter.hpp"

#include <rime/engine.h>
#include <rime/segmentation.h>
#include <rime/service.h>
#include <rime/translation.h>

#include <cassert>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <string>
#include <utility>
#include <vector>

namespace {

using namespace rime;

const std::string kPhrase = "你好世界";
const std::string kSearch = "yunpin-search:nihaoshijie";
const std::string kFavorite = "yunpin-fav:nihaoshijie";
const std::string kInactive = "<filter inactive>";

class FakeUpstream : public Translation {
 public:
  explicit FakeUpstream(std::vector<std::string> items)
      : items_(std::move(items)) {
    set_exhausted(items_.empty());
  }

  bool Next() override {
    if (cursor_ < items_.size()) {
      ++cursor_;
    }
    set_exhausted(cursor_ >= items_.size());
    return !exhausted();
  }

  an<Candidate> Peek() override {
    if (cursor_ >= items_.size()) {
      return nullptr;
    }
    return New<SimpleCandidate>("fake", 0, 5, items_[cursor_]);
  }

 private:
  std::vector<std::string> items_;
  std::size_t cursor_{0};
};

std::vector<std::string> Upstream() {
  return {"u0", "u1", "u2", "u3", "u4", "u5", "u6"};
}

// Owns the stub engine graph a Ticket needs.
struct Harness {
  Config config;
  Schema schema{&config};
  Context context;
  Engine engine;

  Harness() {
    engine.schema_ = &schema;
    engine.context_ = &context;
    schema.set_page_size(8);  // the shipped menu/page_size
    config.bools_["yunpin/enabled"] = true;
    config.ints_["yunpin/max_candidates"] = 2;
  }

  Ticket ticket() {
    Ticket ticket;
    ticket.engine = &engine;
    ticket.schema = &schema;
    ticket.name_space = "yunpin";
    return ticket;
  }
};

void WriteSnapshot(const std::filesystem::path& user_data_dir) {
  std::filesystem::create_directories(user_data_dir / "yunpin");
  std::ofstream out(user_data_dir / "yunpin" / "private.tsv");
  out << "phrase\tpinyin\tsource\tuse_count\tpinned\n";
  out << kPhrase << "\tni hao shi jie\tcodex_history\t9\ttrue\n";
}

// Drives the filter the way librime does: AppliesToSegment, then Apply, then
// walk the returned translation.
std::vector<std::string> Run(YunPinFilter& filter,
                             Harness& harness,
                             const std::string& input,
                             std::size_t limit) {
  harness.context.input_ = input;
  Segment segment(0, static_cast<int>(input.size()));
  segment.tags.insert("abc");
  if (!filter.AppliesToSegment(&segment)) {
    return {kInactive};
  }
  CandidateList candidates;
  auto translation = filter.Apply(New<FakeUpstream>(Upstream()), &candidates);

  std::vector<std::string> texts;
  std::size_t guard = 0;
  while (translation && !translation->exhausted() && texts.size() < limit) {
    assert(++guard < 1000 && "translation failed to terminate");
    auto candidate = translation->Peek();
    texts.push_back(candidate ? candidate->text() : "<null>");
    if (!translation->Next()) {
      break;
    }
  }
  return texts;
}

void Expect(const char* name,
            const std::vector<std::string>& got,
            const std::vector<std::string>& want) {
  if (got == want) {
    return;
  }
  std::cerr << "FAILED: " << name << "\n  got: ";
  for (const auto& text : got) {
    std::cerr << "[" << text << "]";
  }
  std::cerr << "\n  want: ";
  for (const auto& text : want) {
    std::cerr << "[" << text << "]";
  }
  std::cerr << "\n";
  assert(false && "unexpected candidate order");
}

void TestShippedDefaultHasNoActionCandidates() {
  Harness harness;
  YunPinFilter filter(harness.ticket());
  // yunpin/expression_search is absent, exactly as in a stock overlay.
  Expect("shipped default", Run(filter, harness, "nihaoshijie", 9),
         {kPhrase, "u0", "u1", "u2", "u3", "u4", "u5", "u6"});
}

void TestOptedInActionsStayOffTheHeadOfThePage() {
  Harness harness;
  harness.config.bools_["yunpin/expression_search"] = true;
  YunPinFilter filter(harness.ticket());
  // page_size 8: the actions belong in the last two slots of the first page.
  Expect("opted in, page_size 8", Run(filter, harness, "nihaoshijie", 9),
         {kPhrase, "u0", "u1", "u2", "u3", "u4", kSearch, kFavorite, "u5"});
}

void TestActionsAreIndependentOfThePrivateSnapshot() {
  const auto empty = std::filesystem::temp_directory_path() /
                     "yunpin-filter-behaviour" / "no-snapshot";
  std::filesystem::create_directories(empty);
  const auto previous = Service::instance().deployer().user_data_dir;
  Service::instance().deployer().user_data_dir = empty;

  Harness harness;
  harness.config.bools_["yunpin/expression_search"] = true;
  YunPinFilter filter(harness.ticket());
  // A missing snapshot must drop only the private phrases. Before the split
  // it disabled the whole filter, and a present one silently enabled these
  // actions.
  Expect("no snapshot", Run(filter, harness, "nihaoshijie", 9),
         {"u0", "u1", "u2", "u3", "u4", "u5", kSearch, kFavorite, "u6"});

  Service::instance().deployer().user_data_dir = previous;
}

void TestModuleSwitchOverridesTheActions() {
  Harness harness;
  harness.config.bools_["yunpin/enabled"] = false;
  harness.config.bools_["yunpin/expression_search"] = true;
  YunPinFilter filter(harness.ticket());
  // The Windows preview ships yunpin/enabled false and must gain nothing.
  Expect("module switch off", Run(filter, harness, "nihaoshijie", 9),
         {kInactive});
}

void TestPasswordModeDisablesEverything() {
  Harness harness;
  harness.config.bools_["yunpin/expression_search"] = true;
  harness.context.options_["password_mode"] = true;
  YunPinFilter filter(harness.ticket());
  Expect("password mode", Run(filter, harness, "nihaoshijie", 9), {kInactive});
}

void TestSmallerPageStillKeepsTheHeadClear() {
  Harness harness;
  harness.schema.set_page_size(5);
  harness.config.bools_["yunpin/expression_search"] = true;
  YunPinFilter filter(harness.ticket());
  Expect("page_size 5", Run(filter, harness, "nihaoshijie", 7),
         {kPhrase, "u0", "u1", kSearch, kFavorite, "u2", "u3"});
}

void TestPrivateCandidatesStayBounded() {
  Harness harness;
  harness.config.ints_["yunpin/max_candidates"] = 5;  // overlay asks for more
  YunPinFilter filter(harness.ticket());
  const auto texts = Run(filter, harness, "nihaoshijie", 3);
  assert(texts.size() == 3);
  assert(texts[0] == kPhrase);
  assert(texts[1] == "u0");  // the clamp to two is still in force
}

}  // namespace

int main() {
  const auto user_data_dir =
      std::filesystem::temp_directory_path() / "yunpin-filter-behaviour";
  std::filesystem::remove_all(user_data_dir);
  WriteSnapshot(user_data_dir);
  Service::instance().deployer().user_data_dir = user_data_dir;

  TestShippedDefaultHasNoActionCandidates();
  TestOptedInActionsStayOffTheHeadOfThePage();
  TestActionsAreIndependentOfThePrivateSnapshot();
  TestModuleSwitchOverridesTheActions();
  TestPasswordModeDisablesEverything();
  TestSmallerPageStillKeepsTheHeadClear();
  TestPrivateCandidatesStayBounded();

  std::filesystem::remove_all(user_data_dir);
  return 0;
}
