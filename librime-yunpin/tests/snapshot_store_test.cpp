// SPDX-License-Identifier: Apache-2.0
#include "yunpin/snapshot_store.hpp"

#ifdef NDEBUG
#undef NDEBUG
#endif
#include <cassert>
#include <sstream>
#include <string>
#include <thread>
#include <vector>

namespace {

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
  TestAtomicReplacementDuringQueries();
  return 0;
}
