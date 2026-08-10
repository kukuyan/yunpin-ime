// SPDX-License-Identifier: Apache-2.0
#include "yunpin/snapshot_store.hpp"

#ifdef NDEBUG
#undef NDEBUG
#endif
#include <algorithm>
#include <cassert>
#include <sstream>
#include <stdexcept>
#include <string>
#include <thread>
#include <vector>

namespace {

static_assert(yunpin::kMaxPrivateSnapshotEntries == 100000,
              "the reviewed personal vocabulary must fit in one snapshot");

void TestImporterFormatAndPinnedLongPhrase() {
  std::istringstream input(
      "phrase\tpinyin\tsource\tuse_count\tpinned\n"
      "中国石化销售股份有限公司河北石家庄石油分公司\t"
      "zhong guo shi hua xiao shou gu fen you xian gong si he bei shi "
      "jia zhuang shi you fen gong si\tsogou_import\t32\ttrue\n"
      "云拼输入法\tyun pin shu ru fa\tcodex_history\t4\tfalse\n"
      "bad\tnot-a-syllable\ttext\t1\tfalse\n");

  auto parsed = yunpin::ParsePrivateSnapshot(input);
  assert(parsed.header_valid);
  assert(parsed.accepted_rows == 2);
  assert(parsed.rejected_rows == 1);
  assert(parsed.entries.front().pinned);
  assert(parsed.entries.front().origin == yunpin::PhraseOrigin::kImported);

  yunpin::SnapshotStore store;
  store.Replace(std::move(parsed.entries));
  auto initials = store.Query("zgsh", 9);
  assert(initials.size() == 1);
  assert(initials.front().pinned);
  assert(initials.front().text ==
         "中国石化销售股份有限公司河北石家庄石油分公司");
  auto exact = store.Query(
      "zhongguoshihuaxiaoshougufenyouxiangongsihebeishijiazhuangshiyoufengongsi",
      2);
  assert(!exact.empty());
}

void TestHeaderAndLimitsAreClosedByDefault() {
  std::istringstream wrong("text\tpinyin\tcount\nhello\the llo\t1\n");
  auto parsed = yunpin::ParsePrivateSnapshot(wrong);
  assert(!parsed.header_valid);
  assert(parsed.entries.empty());

  std::istringstream malformed(
      "phrase\tpinyin\tsource\tuse_count\n"
      "missing-count\tmei wen ti\ttext\t0\n"
      "missing-pinyin\t\ttext\t2\n");
  parsed = yunpin::ParsePrivateSnapshot(malformed);
  assert(parsed.header_valid);
  assert(parsed.accepted_rows == 0);
  assert(parsed.rejected_rows == 2);
}

void TestDuplicateSnapshotRowsAreRejectedBeforeIndexBuild() {
  std::istringstream duplicate(
      "phrase\tpinyin\tsource\tuse_count\n"
      "云拼输入法\tyun pin shu ru fa\ttext\t2\n"
      "云拼输入法\tyun pin shu ru fa\ttext\t3\n");
  const auto parsed = yunpin::ParsePrivateSnapshot(duplicate);
  assert(parsed.header_valid);
  assert(parsed.accepted_rows == 1);
  assert(parsed.rejected_rows == 1);
}

void TestLegacyPersonalSpellingsStaySnapshotLocal() {
  std::ostringstream snapshot;
  snapshot << "phrase\tpinyin\tsource\tuse_count\n"
           << "synthetic-standard\tyun pin\tsogou_import\t2\n"
           << "synthetic-letter\tb chao\tsogou_import\t3\n"
           << "synthetic-three\tb c chao\tsogou_import\t4\n"
           << "synthetic-fuzzy\tz ao chao\tsogou_import\t5\n"
           << "synthetic-fiao\tfiao\tsogou_import\t6\n"
           << "synthetic-kei\tkei\tsogou_import\t7\n"
           << "synthetic-tei\ttei\tsogou_import\t8\n";
  std::string long_code;
  snapshot << "synthetic-long\t";
  for (std::size_t index = 0; index < 64; ++index) {
    if (index != 0) {
      snapshot << ' ';
    }
    snapshot << 'b';
    long_code.push_back('b');
  }
  snapshot << "\tsogou_import\t9\n"
           << "synthetic-one-letter\tb\tsogou_import\t10\n"
           << "synthetic-unknown\tzzq\tsogou_import\t11\n";
  std::istringstream compatible(snapshot.str());
  auto parsed = yunpin::ParsePrivateSnapshot(compatible);
  assert(parsed.header_valid);
  assert(parsed.accepted_rows == 8);
  assert(parsed.rejected_rows == 2);

  // Even if a caller opts into fuzzy aliases, a snapshot-legacy code can only
  // match its literal complete spelling.
  yunpin::PhraseIndex fuzzy_index(parsed.entries,
                                  yunpin::FuzzyConfig::Common());
  assert(fuzzy_index.Query("zhaochao", 2).empty());
  const auto fuzzy_literal = fuzzy_index.Query("zaochao", 2);
  assert(fuzzy_literal.size() == 1);
  assert(fuzzy_literal.front().text == "synthetic-fuzzy");
  assert(fuzzy_literal.front().match == yunpin::MatchKind::kExactFull);

  yunpin::SnapshotStore store;
  store.Replace(std::move(parsed.entries));
  assert(store.size() == 8);
  assert(!store.Query("yun", 2).empty());
  assert(!store.Query("yp", 2).empty());
  const auto letter = store.Query("bchao", 2);
  assert(letter.size() == 1);
  assert(letter.front().text == "synthetic-letter");
  assert(store.Query("bc", 2).empty());
  assert(store.Query("bcc", 2).empty());
  const auto three = store.Query("bcchao", 2);
  assert(three.size() == 1);
  assert(three.front().text == "synthetic-three");
  assert(three.front().match == yunpin::MatchKind::kExactFull);
  assert(store.Query("bb", 2).empty());
  assert(store.Query("bbb", 2).empty());
  assert(store.Query("b", 2).empty());
  const auto long_exact = store.Query(long_code, 2);
  assert(long_exact.size() == 1);
  assert(long_exact.front().text == "synthetic-long");
  assert(long_exact.front().match == yunpin::MatchKind::kExactFull);
  assert(store.Query("f", 2).empty());
  const auto legacy = store.Query("fiao", 2);
  assert(legacy.size() == 1);
  assert(legacy.front().text == "synthetic-fiao");

  // Compatibility is intentionally not a global Pinyin policy change.
  const yunpin::PinyinSegmenter segmenter;
  assert(!segmenter.IsSyllable("b"));
  assert(!segmenter.IsSyllable("fiao"));
  assert(!segmenter.IsSyllable("kei"));
  assert(!segmenter.IsSyllable("tei"));

  yunpin::PhraseEntry public_entry;
  public_entry.id = "synthetic-public";
  public_entry.text = "synthetic-public";
  public_entry.syllables = {"b", "chao"};
  public_entry.origin = yunpin::PhraseOrigin::kPublic;
  public_entry.use_count = 1;
  public_entry.private_exact_code_only = true;
  bool rejected_public_compatibility = false;
  try {
    const yunpin::PhraseIndex disallowed({std::move(public_entry)});
    (void)disallowed;
  } catch (const std::invalid_argument&) {
    rejected_public_compatibility = true;
  }
  assert(rejected_public_compatibility);

  yunpin::PhraseEntry one_letter_entry;
  one_letter_entry.id = "synthetic-private-one-letter";
  one_letter_entry.text = "synthetic-private-one-letter";
  one_letter_entry.syllables = {"b"};
  one_letter_entry.origin = yunpin::PhraseOrigin::kImported;
  one_letter_entry.use_count = 1;
  one_letter_entry.private_exact_code_only = true;
  bool rejected_one_letter_compatibility = false;
  try {
    const yunpin::PhraseIndex disallowed({std::move(one_letter_entry)});
    (void)disallowed;
  } catch (const std::invalid_argument&) {
    rejected_one_letter_compatibility = true;
  }
  assert(rejected_one_letter_compatibility);

  std::vector<yunpin::PhraseEntry> collisions;
  yunpin::PhraseEntry public_exact;
  public_exact.id = "synthetic-public-exact";
  public_exact.text = "synthetic-public-exact";
  public_exact.syllables = {"bei"};
  public_exact.origin = yunpin::PhraseOrigin::kPublic;
  public_exact.use_count = 100;
  collisions.push_back(std::move(public_exact));
  for (std::size_t index = 0; index < 3; ++index) {
    yunpin::PhraseEntry private_exact;
    private_exact.id = "synthetic-private-exact-" + std::to_string(index);
    private_exact.text = private_exact.id;
    private_exact.syllables = {"b", "e", "i"};
    private_exact.origin = yunpin::PhraseOrigin::kImported;
    private_exact.use_count = 3 - index;
    private_exact.private_exact_code_only = true;
    collisions.push_back(std::move(private_exact));
  }
  const yunpin::PhraseIndex collision_index(std::move(collisions));
  const auto collision = collision_index.Query("bei", 8);
  assert(collision.size() == 3);
  assert(collision[0].is_personal());
  assert(collision[1].is_personal());
  assert(!collision[2].is_personal());
  assert(std::count_if(collision.begin(), collision.end(),
                       [](const auto& candidate) {
                         return candidate.is_personal();
                       }) == 2);
}

void TestRowsBeyondLegacyFiftyThousandLimitAreRetained() {
  constexpr std::size_t kRows = 50001;
  std::ostringstream snapshot;
  snapshot << "phrase\tpinyin\tsource\tuse_count\n";
  for (std::size_t index = 0; index < kRows; ++index) {
    snapshot << "synthetic-" << index << "\tyun pin\tsogou_import\t1\n";
  }
  std::istringstream input(snapshot.str());
  const auto parsed = yunpin::ParsePrivateSnapshot(input);
  assert(parsed.header_valid);
  assert(parsed.accepted_rows == kRows);
  assert(parsed.rejected_rows == 0);
  assert(parsed.entries.size() == kRows);
}

void TestAtomicReplacementDuringQueries() {
  yunpin::SnapshotStore store;
  std::vector<yunpin::PhraseEntry> first{{
      "one", "云拼输入法", {"yun", "pin", "shu", "ru", "fa"},
      yunpin::PhraseOrigin::kPersonal, 4, 0, true, true, false}};
  store.Replace(first);

  std::thread reader([&store]() {
    for (int i = 0; i < 1000; ++i) {
      const auto result = store.Query("ypsr", 2);
      assert(result.size() <= 1);
    }
  });
  std::vector<yunpin::PhraseEntry> second{{
      "two", "云拼候选", {"yun", "pin", "hou", "xuan"},
      yunpin::PhraseOrigin::kHistory, 8, 0, true, true, false}};
  store.Replace(std::move(second));
  reader.join();
  assert(store.size() == 1);
}

}  // namespace

int main() {
  TestImporterFormatAndPinnedLongPhrase();
  TestHeaderAndLimitsAreClosedByDefault();
  TestDuplicateSnapshotRowsAreRejectedBeforeIndexBuild();
  TestLegacyPersonalSpellingsStaySnapshotLocal();
  TestRowsBeyondLegacyFiftyThousandLimitAreRetained();
  TestAtomicReplacementDuringQueries();
  return 0;
}
