// SPDX-License-Identifier: Apache-2.0
#include "yunpin/phrase_engine.hpp"

#include <algorithm>
#include <atomic>
#include <iostream>
#include <stdexcept>
#include <string>
#include <thread>
#include <utility>
#include <vector>

namespace {

using yunpin::Candidate;
using yunpin::PhraseEntry;
using yunpin::PhraseIndex;
using yunpin::PhraseOrigin;

void Check(bool condition, const std::string& message) {
  if (!condition) {
    throw std::runtime_error(message);
  }
}

PhraseEntry Entry(std::string id, std::string text, std::string pinyin,
                  PhraseOrigin origin, std::uint64_t use_count = 0,
                  std::int64_t weight = 0, bool pinned = false,
                  bool learned = false) {
  return PhraseEntry{std::move(id),
                     std::move(text),
                     yunpin::SplitPinyin(pinyin),
                     origin,
                     use_count,
                     weight,
                     pinned,
                     learned,
                     false};
}

bool ContainsId(const std::vector<Candidate>& candidates,
                const std::string& id) {
  return std::any_of(candidates.begin(), candidates.end(),
                     [&](const Candidate& candidate) {
                       return candidate.id == id;
                     });
}

bool ContainsString(const std::vector<std::string>& values,
                    const std::string& expected) {
  return std::find(values.begin(), values.end(), expected) != values.end();
}

bool ContainsPath(const std::vector<std::vector<std::string>>& paths,
                  const std::vector<std::string>& expected) {
  return std::find(paths.begin(), paths.end(), expected) != paths.end();
}

std::size_t PositionOf(const std::vector<Candidate>& candidates,
                       const std::string& id) {
  const auto it = std::find_if(candidates.begin(), candidates.end(),
                               [&](const Candidate& candidate) {
                                 return candidate.id == id;
                               });
  return it == candidates.end()
             ? candidates.size()
             : static_cast<std::size_t>(it - candidates.begin());
}

std::vector<PhraseEntry> AcceptanceEntries() {
  return {
      Entry("company-long",
            "中国石化销售股份有限公司河北石家庄石油分公司",
            "zhong guo shi hua xiao shou gu fen you xian gong si he bei "
            "shi jia zhuang shi you fen gong si",
            PhraseOrigin::kPersonal, 38, 1000, true),
      Entry("china", "中国", "zhong guo", PhraseOrigin::kPublic, 0,
            20000),
      Entry("sinopec", "中国石化", "zhong guo shi hua",
            PhraseOrigin::kPublic, 0, 18000),
      Entry("china-affairs", "中国事务", "zhong guo shi wu",
            PhraseOrigin::kBase, 0, 5000),
      Entry("prc", "中华人民共和国",
            "zhong hua ren min gong he guo", PhraseOrigin::kPublic, 0,
            19000),
  };
}

void TestAcceptanceAndRecallThresholds() {
  PhraseIndex index(AcceptanceEntries());
  const std::string full =
      "zhongguoshihuaxiaoshougufenyouxiangongsihebeishijiazhuangshiyou"
      "fengongsi";

  Check(!ContainsId(index.Query("zhong"), "company-long"),
        "pinned long phrase must wait for two complete syllables");
  Check(!ContainsId(index.Query("zgs"), "company-long"),
        "pinned long phrase must wait for four initials");

  for (const std::string query : {"zhongguo", "zhongguoshihua", "zgsh"}) {
    const auto candidates = index.Query(query);
    Check(PositionOf(candidates, "company-long") < 3,
          "golden organization phrase must rank in the top three for " +
              query);
  }

  const auto exact = index.Query(full);
  Check(!exact.empty() && exact.front().id == "company-long",
        "complete pinyin must rank the complete organization phrase first");
}

void TestPinnedShortPhraseInitialsRemainAvailable() {
  Check(yunpin::kLongPhraseMinSyllables == 4,
        "the long-phrase threshold must stay explicit and reviewable");
  PhraseIndex index({
      Entry("two-syllable", "中国", "zhong guo", PhraseOrigin::kPersonal,
            4, 10, true),
      Entry("three-syllable", "星河湾", "xing he wan",
            PhraseOrigin::kPersonal, 4, 9, true),
      Entry("long-four", "星河数据", "xing he shu ju",
            PhraseOrigin::kPersonal, 4, 8, true),
  });

  Check(ContainsId(index.Query("zg"), "two-syllable"),
        "a two-syllable pin must be recalled by two initials");
  Check(ContainsId(index.Query("xh"), "three-syllable") &&
            ContainsId(index.Query("xhw"), "three-syllable"),
        "a three-syllable pin must be recalled by two or three initials");
  Check(!ContainsId(index.Query("xhs"), "long-four"),
        "a long pin must still wait for four initials");
  Check(ContainsId(index.Query("xhsj"), "long-four"),
        "a long pin must be recalled by four initials");
  Check(!ContainsId(index.Query("xing"), "long-four") &&
            ContainsId(index.Query("xinghe"), "long-four"),
        "a long pin must still wait for two complete full-pinyin syllables");
}

void TestSourcePrecedenceAndFirstPageQuota() {
  PhraseIndex index({
      Entry("pinned", "固定公司", "gong si", PhraseOrigin::kPersonal, 1,
            1, true),
      Entry("imported", "迁移公司", "gong si", PhraseOrigin::kImported,
            90, 800),
      Entry("history", "历史公司", "gong si", PhraseOrigin::kHistory, 80,
            700),
      Entry("personal", "个人公司", "gong si", PhraseOrigin::kPersonal,
            70, 600),
      Entry("public-a", "公共公司甲", "gong si", PhraseOrigin::kPublic, 0,
            1000),
      Entry("public-b", "公共公司乙", "gong si", PhraseOrigin::kPublic, 0,
            900),
      Entry("public-c", "公共公司丙", "gong si", PhraseOrigin::kPublic, 0,
            800),
      Entry("public-d", "公共公司丁", "gong si", PhraseOrigin::kPublic, 0,
            700),
      Entry("base-a", "基础公司甲", "gong si", PhraseOrigin::kBase, 0, 500),
      Entry("base-b", "基础公司乙", "gong si", PhraseOrigin::kBase, 0, 400),
  });

  const auto candidates = index.Query("gongsi", 10);
  Check(candidates.size() == 10, "all ten matching candidates should return");
  Check(candidates[0].id == "pinned", "manual pin must be first");
  Check(candidates[1].id == "imported",
        "highest-frequency personal/import candidate must follow pin");
  Check(std::all_of(candidates.begin() + 2, candidates.begin() + 6,
                    [](const Candidate& candidate) {
                      return candidate.origin == PhraseOrigin::kPublic;
                    }) &&
            std::all_of(candidates.begin() + 6, candidates.begin() + 8,
                        [](const Candidate& candidate) {
                          return candidate.origin == PhraseOrigin::kBase;
                        }),
        "public then base candidates must fill the eight-item first page");
  constexpr std::size_t first_page_limit = 8;
  const std::size_t first_page_personal = static_cast<std::size_t>(
      std::count_if(candidates.begin(), candidates.begin() + first_page_limit,
                    [](const Candidate& candidate) {
                      return candidate.is_personal();
                    }));
  Check(first_page_personal == 2,
        "the first eight candidates must contain at most two personal items");
  Check(candidates[8].id == "history" && candidates[9].id == "personal",
        "deferred personal candidates must retain deterministic frequency order");
}

void TestLearningGateAndTombstones() {
  PhraseIndex index({
      Entry("learned-once", "学习一次", "yun pin", PhraseOrigin::kPersonal,
            1, 100, false, true),
      Entry("learned-twice", "学习两次", "yun pin", PhraseOrigin::kPersonal,
            2, 100, false, true),
      Entry("public", "云拼", "yun pin", PhraseOrigin::kPublic, 0, 90),
  });

  auto candidates = index.Query("yunpin");
  Check(!ContainsId(candidates, "learned-once"),
        "one-use automatically learned phrase must stay hidden");
  Check(ContainsId(candidates, "learned-twice"),
        "two-use automatically learned phrase must be eligible");

  Check(index.ApplyTombstone("learned-twice"),
        "known id should accept a tombstone");
  Check(!index.ApplyTombstone("missing"),
        "unknown id should not report a tombstone update");
  candidates = index.Query("yunpin");
  Check(!ContainsId(candidates, "learned-twice"),
        "remove-wins tombstone must suppress a phrase");
}

void TestConcurrentQueriesAndTombstone() {
  PhraseIndex index({
      Entry("concurrent", "并发删除", "bing fa shan chu",
            PhraseOrigin::kPersonal, 8, 100, true),
      Entry("public", "并发", "bing fa", PhraseOrigin::kPublic, 0, 90),
  });

  constexpr int kWorkerCount = 4;
  std::atomic<int> ready{0};
  std::atomic<int> pre_tombstone_queries{0};
  std::atomic<int> phase{0};
  std::atomic<bool> failed{false};
  std::vector<std::thread> workers;
  workers.reserve(kWorkerCount);
  for (int worker = 0; worker < kWorkerCount; ++worker) {
    workers.emplace_back([&]() {
      ready.fetch_add(1, std::memory_order_release);
      while (phase.load(std::memory_order_acquire) == 0) {
        std::this_thread::yield();
      }
      while (phase.load(std::memory_order_acquire) == 1) {
        (void)index.Query("bingfa");
        pre_tombstone_queries.fetch_add(1, std::memory_order_relaxed);
      }
      for (int iteration = 0; iteration < 1000; ++iteration) {
        if (ContainsId(index.Query("bingfa"), "concurrent")) {
          failed.store(true, std::memory_order_relaxed);
        }
      }
    });
  }

  while (ready.load(std::memory_order_acquire) != kWorkerCount) {
    std::this_thread::yield();
  }
  phase.store(1, std::memory_order_release);
  while (pre_tombstone_queries.load(std::memory_order_acquire) < 100) {
    std::this_thread::yield();
  }
  Check(index.ApplyTombstone("concurrent"),
        "concurrent tombstone target must exist");
  phase.store(2, std::memory_order_release);

  for (std::thread& worker : workers) {
    worker.join();
  }
  Check(!failed.load(std::memory_order_relaxed),
        "queries after the atomic tombstone observed a deleted phrase");
}

void TestNormalizationAndDeterminism() {
  Check(yunpin::NormalizePinyin("L\xC3\x9C-4") .empty(),
        "non-ASCII pinyin is explicitly rejected");
  Check(yunpin::NormalizePinyin("N\xC3\x9C") .empty(),
        "UTF-8 umlaut is outside the ASCII keyboard contract");
  Check(yunpin::NormalizePinyin("LU:4") == "lv",
        "u-colon and tone digits must normalize");

  PhraseIndex index({
      Entry("xian-city", "西安", "xi an", PhraseOrigin::kPublic, 0, 50),
      Entry("first", "先", "xian", PhraseOrigin::kBase, 0, 40),
      Entry("line", "西岸", "xi an", PhraseOrigin::kBase, 0, 30),
  });
  const auto first = index.Query("XI'AN");
  const auto second = index.Query("xi-an");
  Check(first.size() == second.size(),
        "equivalent normalized queries must return equal result counts");
  for (std::size_t i = 0; i < first.size(); ++i) {
    Check(first[i].id == second[i].id,
          "ranking must be deterministic across equivalent input spellings");
  }
  Check(!first.empty() && first.front().id == "xian-city",
        "public exact homophone must precede base candidates");
}

void TestInvalidEntries() {
  bool threw = false;
  try {
    PhraseIndex invalid({Entry("same", "甲", "jia", PhraseOrigin::kBase),
                         Entry("same", "乙", "yi", PhraseOrigin::kBase)});
    (void)invalid;
  } catch (const std::invalid_argument&) {
    threw = true;
  }
  Check(threw, "duplicate ids must be rejected deterministically");
}

void TestBoundedAmbiguousSegmentation() {
  const yunpin::PinyinSegmenter segmenter;
  const auto ambiguous = segmenter.Segment("xian");
  Check(!ambiguous.empty() &&
            ambiguous.front() == std::vector<std::string>{"xian"},
        "longest complete syllable path must be deterministic and first");
  Check(ContainsPath(ambiguous, {"xian"}),
        "xian must be available as one complete syllable");
  Check(ContainsPath(ambiguous, {"xi", "an"}),
        "xian must retain the xi + an ambiguity path");

  const auto explicit_boundary = segmenter.Segment("xi'an");
  Check(explicit_boundary.size() == 1 &&
            explicit_boundary.front() ==
                std::vector<std::string>({"xi", "an"}),
        "apostrophe must force a syllable boundary and suppress xian");

  const yunpin::PinyinSegmenter one_path(
      yunpin::SegmenterLimits{/*max_paths=*/1,
                              /*max_syllables_per_path=*/8,
                              /*max_input_letters=*/32});
  const auto bounded = one_path.Segment("xian");
  Check(bounded.size() == 1 && bounded.front() ==
                                   std::vector<std::string>({"xian"}),
        "segmenter max_paths must be a hard deterministic bound");
  Check(one_path.Segment(std::string(33, 'a')).empty(),
        "segmenter max_input_letters must reject oversized input");
}

void TestConfigurableFuzzyAliasesAndRecall() {
  yunpin::FuzzyConfig common = yunpin::FuzzyConfig::Common();
  common.max_aliases = 32;
  Check(ContainsString(yunpin::ExpandFuzzyAliases("zongguo", common),
                       "zhongguo"),
        "zh/z must expand complete syllables");
  Check(ContainsString(yunpin::ExpandFuzzyAliases("ceng", common), "cheng"),
        "ch/c must expand complete syllables");
  Check(ContainsString(yunpin::ExpandFuzzyAliases("sanghai", common),
                       "shanghai"),
        "sh/s must expand complete syllables");
  Check(ContainsString(yunpin::ExpandFuzzyAliases("lanjing", common),
                       "nanjing"),
        "n/l must expand complete syllables");
  Check(ContainsString(yunpin::ExpandFuzzyAliases("shen", common), "sheng"),
        "en/eng must expand complete syllables");
  Check(ContainsString(yunpin::ExpandFuzzyAliases("jin", common), "jing"),
        "in/ing must expand complete syllables");

  const auto short_one = yunpin::ExpandFuzzyAliases("z", common);
  const auto short_two = yunpin::ExpandFuzzyAliases("si", common);
  Check(short_one == std::vector<std::string>({"z"}) &&
            short_two == std::vector<std::string>({"si"}),
        "one- and two-letter inputs must never receive fuzzy aliases");

  yunpin::FuzzyConfig only_zh_z;
  only_zh_z.zh_z = true;
  Check(!ContainsString(yunpin::ExpandFuzzyAliases("ceng", only_zh_z),
                        "cheng"),
        "disabled fuzzy pairs must not expand");

  const std::vector<PhraseEntry> fuzzy_entries = {
      Entry("china", "中国", "zhong guo", PhraseOrigin::kPublic, 0, 100),
      Entry("literal-zong", "总国", "zong guo", PhraseOrigin::kBase, 0, 10),
      Entry("shanghai", "上海", "shang hai", PhraseOrigin::kPublic, 0, 90),
      Entry("nanjing", "南京", "nan jing", PhraseOrigin::kPublic, 0, 80),
      Entry("city", "城市", "cheng shi", PhraseOrigin::kPublic, 0, 70),
      Entry("sound", "声音", "sheng yin", PhraseOrigin::kPublic, 0, 60),
      Entry("capital", "北京", "bei jing", PhraseOrigin::kPublic, 0, 50),
      Entry("shi", "是", "shi", PhraseOrigin::kPublic, 0, 40),
  };

  PhraseIndex literal_only(fuzzy_entries);
  Check(!ContainsId(literal_only.Query("sanghai"), "shanghai"),
        "default index must preserve disabled fuzzy behavior");

  PhraseIndex fuzzy_index(fuzzy_entries, common);
  Check(ContainsId(fuzzy_index.Query("zongguo"), "china"),
        "configured index must use zh/z aliases for recall");
  Check(fuzzy_index.Query("zongguo").front().id == "literal-zong",
        "literal exact spelling must outrank a fuzzy alias");
  Check(ContainsId(fuzzy_index.Query("sanghai"), "shanghai") &&
            ContainsId(fuzzy_index.Query("lanjing"), "nanjing") &&
            ContainsId(fuzzy_index.Query("cengshi"), "city") &&
            ContainsId(fuzzy_index.Query("shenyin"), "sound") &&
            ContainsId(fuzzy_index.Query("beijin"), "capital"),
        "configured index must recall all common fuzzy-pair examples");
  Check(!ContainsId(fuzzy_index.Query("si"), "shi"),
        "two-letter sh/s input must not pollute candidates");
}

}  // namespace

int main() {
  try {
    TestAcceptanceAndRecallThresholds();
    TestPinnedShortPhraseInitialsRemainAvailable();
    TestSourcePrecedenceAndFirstPageQuota();
    TestLearningGateAndTombstones();
    TestConcurrentQueriesAndTombstone();
    TestNormalizationAndDeterminism();
    TestInvalidEntries();
    TestBoundedAmbiguousSegmentation();
    TestConfigurableFuzzyAliasesAndRecall();
    std::cout << "phrase_engine_tests: PASS\n";
    return 0;
  } catch (const std::exception& error) {
    std::cerr << "phrase_engine_tests: FAIL: " << error.what() << '\n';
    return 1;
  }
}
