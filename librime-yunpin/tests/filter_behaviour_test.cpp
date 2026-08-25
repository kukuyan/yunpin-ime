// SPDX-License-Identifier: Apache-2.0
//
// Exercises the real YunPinFilter against the stub librime headers in
// rime_stubs/. The filter, the snapshot store and the phrase engine are the
// production sources; only librime is replaced. This keeps the candidate
// ordering rules testable on a machine that cannot build librime.
//
// The rule these cases exist to protect: expression actions are not injected
// until a native frontend has a typed, explicitly armed channel. A stale or
// manually edited expression_search setting must not turn ordinary candidate
// text into a browser or file-system command.

// Assertions must survive a Release configuration.
#undef NDEBUG

#include "rime_yunpin_filter.hpp"
#include "yunpin/native_selection_events.hpp"
#include "yunpin/replay_native.hpp"

#include <rime/engine.h>
#include <rime/key_event.h>
#include <rime/segmentation.h>
#include <rime/service.h>
#include <rime/translation.h>

#include <cassert>
#include <cstdint>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <memory>
#include <string>
#include <utility>
#include <vector>

namespace {

using namespace rime;

const std::string kPhrase = "你好世界";
const std::string kLongPrivatePhrase = "长期个人候选";
const std::string kPrivateFirst = "双个人候选一";
const std::string kPrivateSecond = "双个人候选二";
const std::string kOffice = "办公室";
const std::string kOfficeWrong = "办公是";
const std::string kSearchLikeText = "yunpin-search:nihaoshijie";
const std::string kFavoriteLikeText = "yunpin-fav:nihaoshijie";
const std::string kInactive = "<filter inactive>";

struct FakeItem {
  std::string text;
  bool correction{false};
};

FakeItem Ordinary(std::string text) {
  return FakeItem{std::move(text), false};
}

FakeItem Correction(std::string text) {
  return FakeItem{std::move(text), true};
}

class FakeUpstream : public Translation {
 public:
  explicit FakeUpstream(std::vector<std::string> items)
      : items_() {
    items_.reserve(items.size());
    for (std::string& item : items) {
      items_.push_back(Ordinary(std::move(item)));
    }
    set_exhausted(items_.empty());
  }

  explicit FakeUpstream(std::vector<FakeItem> items)
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
    auto candidate =
        New<SimpleCandidate>("fake", 0, 5, items_[cursor_].text);
    candidate->set_correction(items_[cursor_].correction);
    return candidate;
  }

 private:
  std::vector<FakeItem> items_;
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
    config.bools_["yunpin/short_input_guard"] = true;
    config.bools_["yunpin/long_correction_guard"] = true;
    config.bools_["yunpin/session_learning"] = true;
    context.options_["yunpin_learning_allowed"] = true;
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

void WriteSnapshot(const std::filesystem::path& user_data_dir,
                   std::int32_t office_wrong_score = 0,
                   std::int32_t office_score = 0) {
  std::filesystem::create_directories(user_data_dir / "yunpin");
  std::ofstream out(user_data_dir / "yunpin" / "private.tsv");
  out << "phrase\tpinyin\tsource\tuse_count\tpinned\tlast_used_day"
         "\tcorrection_score\n";
  out << kPhrase << "\tni hao shi jie\tcodex_history\t9\ttrue\t0\t0\n";
  out << kLongPrivatePhrase
      << "\tchang qi ge ren hou xuan\tcodex_history\t8\ttrue\t0\t0\n";
  out << kPrivateFirst
      << "\tshuang ge ren hou xuan\tcodex_history\t10\ttrue\t0\t0\n";
  out << kPrivateSecond
      << "\tshuang ge ren hou xuan\tcodex_history\t9\ttrue\t0\t0\n";
  out << kOfficeWrong
      << "\tban gong shi\tsynced_learning@20679\t5\tfalse\t20679\t"
      << office_wrong_score << "\n";
  out << kOffice
      << "\tban gong shi\tsogou_sgpybin\t165\tfalse\t0\t"
      << office_score << "\n";
}

void EmitCommit(Harness& harness,
                const std::string& input,
                const std::string& text,
                const std::string& type = "phrase",
                bool trailing_placeholder = true) {
  harness.context.input_ = input;
  harness.context.commit_text_ = text;
  harness.context.composition_.clear();
  Segment segment(0, static_cast<int>(input.size()));
  segment.status = Segment::kSelected;
  segment.selected_candidate_ = New<SimpleCandidate>(
      type, 0, input.size(), text);
  harness.context.composition_.push_back(std::move(segment));
  if (trailing_placeholder) {
    harness.context.composition_.push_back(
        Segment(static_cast<int>(input.size()),
                static_cast<int>(input.size())));
  }
  harness.context.commit_notifier()(&harness.context);

  // Context::Commit clears composition after the real notifier returns.
  harness.context.input_.clear();
  harness.context.commit_text_.clear();
  harness.context.composition_.clear();
  harness.context.update_notifier()(&harness.context);
}

void EmitMultiSegmentCommit(Harness& harness,
                            const std::string& input,
                            const std::string& text) {
  harness.context.input_ = input;
  harness.context.commit_text_ = text;
  harness.context.composition_.clear();
  Segment first(0, 2);
  first.status = Segment::kConfirmed;
  first.selected_candidate_ = New<SimpleCandidate>("phrase", 0, 2, "日");
  Segment second(2, static_cast<int>(input.size()));
  second.status = Segment::kSelected;
  second.selected_candidate_ = New<SimpleCandidate>(
      "phrase", 2, input.size(), "长");
  harness.context.composition_.push_back(std::move(first));
  harness.context.composition_.push_back(std::move(second));
  harness.context.commit_notifier()(&harness.context);
  harness.context.input_.clear();
  harness.context.commit_text_.clear();
  harness.context.composition_.clear();
  harness.context.update_notifier()(&harness.context);
}

void EmitComposition(Harness& harness, const std::string& input) {
  harness.context.input_ = input;
  harness.context.update_notifier()(&harness.context);
}

void EmitKey(Harness& harness, int keycode, int modifier = 0) {
  const KeyEvent key(keycode, modifier);
  harness.context.unhandled_key_notifier()(&harness.context, key);
}

void DrainNativeSelectionEvents() {
  yunpin::NativeSelectionEvent event;
  while (yunpin::NativeSelectionEventQueue::Instance().TryPop(&event)) {
  }
}

std::vector<std::string> DrainReplayEvents() {
  std::vector<std::string> events;
  char json[yunpin::kReplayJsonLimit + 1]{};
  auto& producer = yunpin::GlobalReplayNativeProducer();
  while (const std::size_t size = producer.DrainJson(json, sizeof(json))) {
    events.emplace_back(json, size);
  }
  return events;
}

// Drives the filter the way librime does: AppliesToSegment, then Apply, then
// walk the returned translation.
std::vector<std::string> RunWithUpstream(
    YunPinFilter& filter,
    Harness& harness,
    const std::string& input,
    std::vector<std::string> upstream,
    std::size_t limit) {
  harness.context.input_ = input;
  Segment segment(0, static_cast<int>(input.size()));
  segment.tags.insert("abc");
  if (!filter.AppliesToSegment(&segment)) {
    return {kInactive};
  }
  CandidateList candidates;
  auto translation =
      filter.Apply(New<FakeUpstream>(std::move(upstream)), &candidates);

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

std::vector<std::string> RunWithTaggedUpstream(
    YunPinFilter& filter,
    Harness& harness,
    const std::string& input,
    std::vector<FakeItem> upstream,
    std::size_t limit) {
  harness.context.input_ = input;
  Segment segment(0, static_cast<int>(input.size()));
  segment.tags.insert("abc");
  if (!filter.AppliesToSegment(&segment)) {
    return {kInactive};
  }
  CandidateList candidates;
  auto translation =
      filter.Apply(New<FakeUpstream>(std::move(upstream)), &candidates);

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

std::vector<std::string> Run(YunPinFilter& filter,
                             Harness& harness,
                             const std::string& input,
                             std::size_t limit) {
  return RunWithUpstream(filter, harness, input, Upstream(), limit);
}

bool SelectVisibleCandidateByText(YunPinFilter& filter,
                                  Harness& harness,
                                  const std::string& input,
                                  const std::string& selected_text,
                                  std::vector<std::string> upstream) {
  harness.context.input_ = input;
  Segment query_segment(0, static_cast<int>(input.size()));
  query_segment.tags.insert("abc");
  if (!filter.AppliesToSegment(&query_segment)) {
    return false;
  }
  CandidateList candidates;
  auto translation =
      filter.Apply(New<FakeUpstream>(std::move(upstream)), &candidates);
  while (translation && !translation->exhausted()) {
    const auto selected = translation->Peek();
    if (selected && selected->text() == selected_text) {
      harness.context.commit_text_ = selected_text;
      harness.context.composition_.clear();
      Segment committed(0, static_cast<int>(input.size()));
      committed.status = Segment::kSelected;
      committed.selected_candidate_ = selected;
      harness.context.composition_.push_back(std::move(committed));
      harness.context.composition_.push_back(
          Segment(static_cast<int>(input.size()),
                  static_cast<int>(input.size())));
      harness.context.commit_notifier()(&harness.context);
      harness.context.input_.clear();
      harness.context.commit_text_.clear();
      harness.context.composition_.clear();
      harness.context.update_notifier()(&harness.context);
      return true;
    }
    if (!translation->Next()) {
      break;
    }
  }
  return false;
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

void TestExpressionConfigCannotArmActions() {
  Harness harness;
  harness.config.bools_["yunpin/expression_search"] = true;
  YunPinFilter filter(harness.ticket());
  Expect("unarmed expression config", Run(filter, harness, "nihaoshijie", 9),
         {kPhrase, "u0", "u1", "u2", "u3", "u4", "u5", "u6"});
}

void TestExpressionConfigCannotArmActionsWithShortUpstream() {
  Harness harness;
  harness.config.bools_["yunpin/expression_search"] = true;
  YunPinFilter filter(harness.ticket());

  // Empty, one-candidate and short upstream translations previously promoted
  // deferred actions into the first two slots. With no typed/armed frontend
  // channel, all three cases must contain only personal/ordinary candidates.
  Expect("empty upstream",
         RunWithUpstream(filter, harness, "nihaoshijie", {}, 8), {kPhrase});
  Expect("one upstream",
         RunWithUpstream(filter, harness, "nihaoshijie", {"u0"}, 8),
         {kPhrase, "u0"});
  Expect("short upstream",
         RunWithUpstream(filter, harness, "nihaoshijie",
                         {"u0", "u1", "u2", "u3"}, 8),
         {kPhrase, "u0", "u1", "u2", "u3"});
}

void TestCommandLikeOrdinaryTextStaysOrdinary() {
  Harness harness;
  harness.config.bools_["yunpin/expression_search"] = true;
  YunPinFilter filter(harness.ticket());
  Expect("command-like text is data",
         RunWithUpstream(filter, harness, "nihaoshijie",
                         {kSearchLikeText, kFavoriteLikeText}, 4),
         {kPhrase, kSearchLikeText, kFavoriteLikeText});
}

void TestShortPinyinSuppressesOnlyLongPureCjkUpstream() {
  Harness harness;
  YunPinFilter filter(harness.ticket());
  const std::string malformed_after_three_cjk =
      std::string("合并为") + std::string("\xe5", 1);

  // Consecutive long predictions, including one at the end, exercise both
  // ordering and termination. A malformed candidate that begins with three
  // valid Han scalars must be retained because the complete UTF-8 value is not
  // valid and therefore cannot be classified safely.
  Expect("he filters only long pure CJK",
         RunWithUpstream(filter, harness, "he",
                         {"合并为", "和", "合并", "hello", "合并A",
                          malformed_after_three_cjk, "中国石化", "tail"},
                         8),
         {"和", "合并", "hello", "合并A", malformed_after_three_cjk,
          "tail"});
  Expect("h filters all-long upstream to exhaustion",
         RunWithUpstream(filter, harness, "h", {"合并为", "中国石化"}, 4),
         {});
}

void TestLongerInputAndPrivateDedupStayUnchanged() {
  Harness harness;
  YunPinFilter filter(harness.ticket());
  Expect("three-letter input keeps long CJK",
         RunWithUpstream(filter, harness, "heb", {"合并为", "tail"}, 4),
         {"合并为", "tail"});
  Expect("private candidate still deduplicates upstream",
         RunWithUpstream(filter, harness, "nihaoshijie",
                         {kPhrase, "中国石化", "tail"}, 5),
         {kPhrase, "中国石化", "tail"});
}

void TestLongCorrectionGuardIsConservativeAndPageBounded() {
  {
    Harness harness;
    YunPinFilter filter(harness.ticket());
    Expect("one long correction may use only total rank two",
           RunWithTaggedUpstream(
               filter, harness, "changshuruhuigui",
               {Correction("c0"), Ordinary("o0"), Correction("c1"),
                Ordinary("o1"), Correction("c2"), Ordinary("o2")},
               8),
           {"o0", "c0", "o1", "o2"});
  }
  {
    Harness harness;
    YunPinFilter filter(harness.ticket());
    Expect("personal head moves one correction to total rank three",
           RunWithTaggedUpstream(
               filter, harness, "changqigerenhouxuan",
               {Correction("c0"), Ordinary("o0"), Correction("c1"),
                Ordinary("o1")},
               8),
           {kLongPrivatePhrase, "o0", "c0", "o1"});
  }
  {
    Harness harness;
    YunPinFilter filter(harness.ticket());
    Expect("two personal heads leave no safe correction slot",
           RunWithTaggedUpstream(
               filter, harness, "shuanggerenhouxuan",
               {Correction("c0"), Ordinary("o0"), Ordinary("o1")}, 8),
           {kPrivateFirst, kPrivateSecond, "o0", "o1"});
  }
  {
    Harness harness;
    YunPinFilter filter(harness.ticket());
    Expect("correction-only upstream fails closed",
           RunWithTaggedUpstream(filter, harness, "changshuruhuigui",
                                 {Correction("c0"), Correction("c1")}, 8),
           {});
  }
  {
    Harness harness;
    YunPinFilter filter(harness.ticket());
    Expect("correction after total rank eight is discarded",
           RunWithTaggedUpstream(
               filter, harness, "changshuruhuigui",
               {Ordinary("o0"), Ordinary("o1"), Ordinary("o2"),
                Ordinary("o3"), Ordinary("o4"), Ordinary("o5"),
                Ordinary("o6"), Ordinary("o7"), Correction("c8"),
                Ordinary("tail")},
               10),
           {"o0", "o1", "o2", "o3", "o4", "o5", "o6", "o7",
            "tail"});
  }
}

void TestSessionCorrectionReranksBoundedUpstreamWindow() {
  Harness harness;
  YunPinFilter filter(harness.ticket());
  EmitCommit(harness, "richang", "日长");
  EmitKey(harness, XK_BackSpace);
  EmitKey(harness, XK_BackSpace);
  EmitComposition(harness, "richang");
  EmitCommit(harness, "richang", "日常");

  Expect("session correction reranks same pinyin",
         RunWithUpstream(filter, harness, "richang",
                         {"日长", "日历", "日常", "tail"}, 6),
         {"日常", "日历", "tail", "日长"});
  const auto stats = filter.QueryHabits(
      yunpin::HabitQuery{"", true, 8});
  assert(stats.size() == 2);
  assert((stats[0].phrase == "日常" || stats[1].phrase == "日常"));
  assert((stats[0].phrase == "日长" || stats[1].phrase == "日长"));
}

void TestShippedOverlayLearnsSelectedYunPinHomophoneImmediately() {
  DrainNativeSelectionEvents();
  Harness harness;
  // This is the shipped profile: the platform host supplies the positive
  // per-session capability after schema construction while the legacy schema
  // learning switch remains false.
  harness.config.bools_["yunpin/session_learning"] = false;
  YunPinFilter filter(harness.ticket());

  const std::vector<std::string> upstream = {kOfficeWrong, kOffice, "tail"};
  Expect("stale snapshot initially keeps the wrong homophone first",
         RunWithUpstream(filter, harness, "bangongshi", upstream, 4),
         {kOfficeWrong, kOffice, "tail"});
  assert(SelectVisibleCandidateByText(filter, harness, "bangongshi", kOffice,
                                      upstream));
  Expect("one explicit office selection immediately becomes first",
         RunWithUpstream(filter, harness, "bangongshi", upstream, 4),
         {kOffice, kOfficeWrong, "tail"});

  yunpin::NativeSelectionEvent event;
  assert(yunpin::NativeSelectionEventQueue::Instance().TryPop(&event));
  assert(event.phrase == kOffice);
  assert(event.pinyin == "bangongshi");
  DrainNativeSelectionEvents();
}

void TestPersistentRimeHomophoneOrderOverridesStaleSnapshotOrder() {
  Harness harness;
  harness.config.bools_["yunpin/session_learning"] = false;
  YunPinFilter filter(harness.ticket());
  Expect("fresh Rime userdb order wins among the same injected homophones",
         RunWithUpstream(filter, harness, "bangongshi",
                         {kOffice, kOfficeWrong, "tail"}, 4),
         {kOffice, kOfficeWrong, "tail"});
}

void TestPersistedCorrectionOverridesStaleUpstreamOrderAfterRestart() {
  const auto user_data_dir = Service::instance().deployer().user_data_dir;
  WriteSnapshot(user_data_dir, -1, 1);
  Harness harness;
  harness.config.bools_["yunpin/session_learning"] = false;
  YunPinFilter filter(harness.ticket());
  Expect("persisted correction score survives upstream alignment",
         RunWithUpstream(filter, harness, "bangongshi",
                         {kOfficeWrong, kOffice, "tail"}, 4),
         {kOffice, kOfficeWrong, "tail"});
  WriteSnapshot(user_data_dir);
}

void TestSessionBridgeFailsClosedOnUnprovenDeletion() {
  {
    Harness harness;
    YunPinFilter filter(harness.ticket());
    EmitCommit(harness, "richang", "日长");
    EmitKey(harness, XK_BackSpace);
    EmitComposition(harness, "richang");
    EmitCommit(harness, "richang", "日常");
    Expect("latest explicit selection reranks without correction proof",
           RunWithUpstream(filter, harness, "richang", {"日长", "日常"},
                           4),
           {"日常", "日长"});
    assert(filter.QueryHabits(
               yunpin::HabitQuery{"", true, 8}).empty());
  }
  {
    Harness harness;
    YunPinFilter filter(harness.ticket());
    EmitCommit(harness, "richang", "日长");
    EmitKey(harness, XK_BackSpace, kControlMask);
    EmitKey(harness, XK_BackSpace);
    EmitKey(harness, XK_BackSpace);
    EmitCommit(harness, "richang", "日常");
    Expect("modified Backspace prevents correction but keeps selection recall",
           RunWithUpstream(filter, harness, "richang", {"日长", "日常"},
                           4),
           {"日常", "日长"});
    assert(filter.QueryHabits(
               yunpin::HabitQuery{"", true, 8}).empty());
  }
  {
    Harness harness;
    harness.context.options_["yunpin_one_shot"] = true;
    YunPinFilter filter(harness.ticket());
    EmitCommit(harness, "richang", "日长");
    EmitKey(harness, XK_BackSpace);
    EmitKey(harness, XK_BackSpace);
    harness.context.options_["yunpin_one_shot"] = false;
    EmitCommit(harness, "richang", "日常");
    assert(filter.QueryHabits(
               yunpin::HabitQuery{"", true, 8}).empty());
  }
  for (const std::string& rejected_type :
       {std::string("sentence"), std::string("yunpin"),
        std::string("unknown")}) {
    Harness harness;
    YunPinFilter filter(harness.ticket());
    EmitCommit(harness, "richang", "日长", rejected_type);
    EmitKey(harness, XK_BackSpace);
    EmitKey(harness, XK_BackSpace);
    EmitCommit(harness, "richang", "日常");
    assert(filter.QueryHabits(
               yunpin::HabitQuery{"", true, 8}).empty());
  }
  {
    Harness harness;
    YunPinFilter filter(harness.ticket());
    EmitMultiSegmentCommit(harness, "richang", "日长");
    EmitKey(harness, XK_BackSpace);
    EmitKey(harness, XK_BackSpace);
    EmitCommit(harness, "richang", "日常");
    assert(filter.QueryHabits(
               yunpin::HabitQuery{"", true, 8}).empty());
  }
}

void TestLearningCallbacksDoNotOutliveFilter() {
  Harness harness;
  auto filter = std::make_unique<YunPinFilter>(harness.ticket());
  EmitCommit(harness, "richang", "日长");
  filter.reset();

  // Every notifier still exists on Context, but all YunPin slots must already
  // be disconnected.  Calling them after filter destruction must be inert.
  harness.context.commit_notifier()(&harness.context);
  harness.context.update_notifier()(&harness.context);
  harness.context.unhandled_key_notifier()(
      &harness.context, KeyEvent(XK_BackSpace, 0));
  harness.context.option_update_notifier()(&harness.context, "ascii_mode");
  harness.context.delete_notifier()(&harness.context);
}

void TestNestedNotifierCanDestroyFilter() {
  Harness harness;
  std::unique_ptr<YunPinFilter> filter;
  // This slot runs before YunPin's slot and simulates an IMK/TSF host tearing
  // down the component from a nested callback.  The disconnected weak slot
  // that follows must not dereference either the filter or its learning state.
  harness.context.commit_notifier().connect(
      [&filter](Context*) { filter.reset(); });
  filter = std::make_unique<YunPinFilter>(harness.ticket());
  EmitCommit(harness, "richang", "日长");
  assert(!filter);
}

void TestRapidSessionLearningChurn() {
  for (int iteration = 0; iteration < 256; ++iteration) {
    Harness harness;
    auto filter = std::make_unique<YunPinFilter>(harness.ticket());
    EmitCommit(harness, "richang", "日长");
    EmitKey(harness, XK_BackSpace);
    harness.context.option_update_notifier()(&harness.context, "ascii_mode");
    harness.context.delete_notifier()(&harness.context);
    filter.reset();
    harness.context.update_notifier()(&harness.context);
  }
}

void TestProtectedContextsPublishNoNativeEvents() {
  DrainNativeSelectionEvents();
  {
    Harness missing_host_capability;
    missing_host_capability.context.options_.erase("yunpin_learning_allowed");
    YunPinFilter filter(missing_host_capability.ticket());
    EmitCommit(missing_host_capability, "weizhishuru", "未知输入");
  }
  {
    Harness unknown_host_context;
    unknown_host_context.context.options_["yunpin_learning_allowed"] = false;
    YunPinFilter filter(unknown_host_context.ticket());
    EmitCommit(unknown_host_context, "weizhishuru", "未知输入");
  }
  for (const std::string& option :
       {std::string("password_mode"), std::string("yunpin_private_mode"),
        std::string("yunpin_one_shot")}) {
    Harness harness;
    harness.context.options_[option] = true;
    YunPinFilter filter(harness.ticket());
    EmitCommit(harness, "yinsici", "隐私词");
  }
  yunpin::NativeSelectionEvent event;
  assert(!yunpin::NativeSelectionEventQueue::Instance().TryPop(&event));

  Harness normal;
  YunPinFilter filter(normal.ticket());
  EmitCommit(normal, "shujuku", "数据库");
  assert(yunpin::NativeSelectionEventQueue::Instance().TryPop(&event));
  assert(event.phrase == "数据库");
  assert(event.pinyin == "shujuku");
  DrainNativeSelectionEvents();
}

void TestReplayCaptureIsExplicitAndUsesTheVisibleCandidatePage() {
  auto& producer = yunpin::GlobalReplayNativeProducer();
  producer.SetEnabled(false);
  (void)producer.DiscardAll();

  Harness disabled;
  disabled.config.bools_["yunpin/enabled"] = false;
  disabled.config.bools_["yunpin/short_input_guard"] = false;
  disabled.config.bools_["yunpin/long_correction_guard"] = false;
  YunPinFilter disabled_filter(disabled.ticket());
  (void)RunWithTaggedUpstream(
      disabled_filter, disabled, "synthetic",
      {Correction("synthetic correction"), Ordinary("synthetic exact")}, 2);
  assert(DrainReplayEvents().empty() &&
         "disabled Replay Lab captured a candidate page");

  producer.SetEnabled(false);
  Harness normal;
  normal.config.bools_["yunpin/enabled"] = false;
  normal.config.bools_["yunpin/session_learning"] = false;
  normal.config.bools_["yunpin/short_input_guard"] = false;
  normal.config.bools_["yunpin/long_correction_guard"] = false;
  YunPinFilter normal_filter(normal.ticket());
  producer.SetEnabled(true);
  (void)RunWithTaggedUpstream(
      normal_filter, normal, "synthetic",
      {Correction("synthetic correction"), Ordinary("synthetic exact")}, 2);
  const auto captured = DrainReplayEvents();
  assert(captured.size() == 1);
  assert(captured[0].find("\"type\":\"composition_snapshot\"") !=
         std::string::npos);
  assert(captured[0].find("\"text\":\"synthetic correction\",\"is_correction\":true,\"highlighted\":true") !=
         std::string::npos);
  assert(captured[0].find("\"text\":\"synthetic exact\",\"is_correction\":false") !=
         std::string::npos);

  Harness refilled;
  refilled.config.bools_["yunpin/enabled"] = false;
  refilled.config.bools_["yunpin/session_learning"] = false;
  refilled.config.bools_["yunpin/short_input_guard"] = false;
  refilled.config.bools_["yunpin/long_correction_guard"] = true;
  YunPinFilter refilled_filter(refilled.ticket());
  (void)RunWithTaggedUpstream(
      refilled_filter, refilled, "changshuruhuigui",
      {Correction("c0"), Correction("c1"), Ordinary("o0"),
       Correction("c2"), Ordinary("o1"), Correction("c3"),
       Ordinary("o2"), Correction("c4"), Ordinary("refilled-visible"),
       Ordinary("tail")},
      10);
  const auto refilled_capture = DrainReplayEvents();
  assert(refilled_capture.size() == 1);
  assert(refilled_capture[0].find("\"text\":\"refilled-visible\"") !=
         std::string::npos &&
         "Replay Lab candidate page did not include the visible refill");

  Harness protected_context;
  protected_context.context.options_["password_mode"] = true;
  protected_context.config.bools_["yunpin/enabled"] = false;
  protected_context.config.bools_["yunpin/short_input_guard"] = false;
  protected_context.config.bools_["yunpin/long_correction_guard"] = false;
  YunPinFilter protected_filter(protected_context.ticket());
  (void)RunWithUpstream(protected_filter, protected_context, "secret",
                        {"first", "second"}, 2);
  assert(DrainReplayEvents().empty() &&
         "protected context published Replay Lab text");

  producer.SetEnabled(false);
  (void)producer.DiscardAll();
}

void TestReplayCommitCarriesTheSelectedVisibleRank() {
  auto& producer = yunpin::GlobalReplayNativeProducer();
  producer.SetEnabled(true);
  (void)producer.DiscardAll();

  Harness harness;
  harness.config.bools_["yunpin/enabled"] = false;
  harness.config.bools_["yunpin/short_input_guard"] = false;
  harness.config.bools_["yunpin/long_correction_guard"] = false;
  YunPinFilter filter(harness.ticket());
  (void)RunWithUpstream(filter, harness, "synthetic", {"first", "second"},
                        2);
  EmitCommit(harness, "synthetic", "second");
  const auto captured = DrainReplayEvents();
  assert(captured.size() == 3);
  assert(captured[1].find("\"type\":\"select\"") != std::string::npos);
  assert(captured[1].find("\"rank\":2") != std::string::npos);
  assert(captured[2].find("\"type\":\"commit\"") != std::string::npos);
  assert(captured[2].find("\"final_text\":\"second\"") !=
         std::string::npos);

  producer.SetEnabled(false);
  (void)producer.DiscardAll();
}

void TestMissingSnapshotKeepsActionsOffAndShortGuardAvailable() {
  const auto empty = std::filesystem::temp_directory_path() /
                     "yunpin-filter-behaviour" / "no-snapshot";
  std::filesystem::create_directories(empty);
  const auto previous = Service::instance().deployer().user_data_dir;
  Service::instance().deployer().user_data_dir = empty;

  Harness harness;
  harness.config.bools_["yunpin/expression_search"] = true;
  YunPinFilter filter(harness.ticket());
  Expect("no snapshot long input is transparent",
         Run(filter, harness, "nihaoshijie", 9),
         {"u0", "u1", "u2", "u3", "u4", "u5", "u6"});
  Expect("no snapshot short guard",
         RunWithUpstream(filter, harness, "he", {"合并为", "和", "tail"},
                         4),
         {"和", "tail"});

  Service::instance().deployer().user_data_dir = previous;
}

void TestPrivateSwitchDoesNotDisableShortGuard() {
  Harness harness;
  harness.config.bools_["yunpin/enabled"] = false;
  harness.config.bools_["yunpin/session_learning"] = false;
  harness.config.bools_["yunpin/expression_search"] = true;
  YunPinFilter filter(harness.ticket());
  Expect("private switch off leaves long input transparent",
         RunWithUpstream(filter, harness, "nihaoshijie", {"中国石化", "tail"},
                         4),
         {"中国石化", "tail"});
  Expect("private switch off keeps short guard",
         RunWithUpstream(filter, harness, "he", {"合并为", "和", "tail"}, 4),
         {"和", "tail"});

  EmitCommit(harness, "richang", "日长");
  EmitKey(harness, XK_BackSpace);
  EmitKey(harness, XK_BackSpace);
  EmitCommit(harness, "richang", "日常");
  Expect("Windows guard-only mode keeps learning disabled",
         RunWithUpstream(filter, harness, "richang", {"日长", "日常"}, 4),
         {"日长", "日常"});
  assert(filter.QueryHabits().empty());
}

void TestBothFeatureSwitchesOffStayInactive() {
  Harness harness;
  harness.config.bools_["yunpin/enabled"] = false;
  harness.config.bools_["yunpin/short_input_guard"] = false;
  harness.config.bools_["yunpin/long_correction_guard"] = false;
  harness.config.bools_["yunpin/session_learning"] = false;
  harness.config.bools_["yunpin/expression_search"] = true;
  YunPinFilter filter(harness.ticket());
  Expect("all filter features off", Run(filter, harness, "he", 9),
         {kInactive});
}

// A protected context must expose no personal data. It must NOT also disable
// the public-data-only ordering guards: those never read the snapshot or the
// learning state, so a password field has no reason to receive worse candidate
// ordering than an ordinary one. Asserting that property, rather than the older
// "the filter is entirely inactive" mechanism, keeps the privacy contract exact
// while letting the guards through.
void TestProtectedContextsExposeNoPersonalDataButKeepPublicGuards() {
  struct ProtectedContext {
    const char* description;
    const char* option;  // nullptr means "host capability absent"
  };
  const ProtectedContext contexts[] = {
      {"password mode", "password_mode"},
      {"private mode", "yunpin_private_mode"},
      {"incognito mode", "incognito_mode"},
      {"one shot", "yunpin_one_shot"},
      {"missing host capability", nullptr},
  };

  for (const ProtectedContext& protected_context : contexts) {
    DrainNativeSelectionEvents();
    Harness harness;
    harness.config.bools_["yunpin/expression_search"] = true;
    if (protected_context.option == nullptr) {
      harness.context.options_.erase("yunpin_learning_allowed");
    } else {
      harness.context.options_[protected_context.option] = true;
    }
    YunPinFilter filter(harness.ticket());

    // No private snapshot phrase and no expression action may become visible.
    for (const std::string& text : Run(filter, harness, "nihaoshijie", 9)) {
      const bool personal =
          text == kPhrase || text == kLongPrivatePhrase ||
          text == kPrivateFirst || text == kPrivateSecond ||
          text == kSearchLikeText || text == kFavoriteLikeText;
      if (personal) {
        std::cout << "FAILED: " << protected_context.description
                  << " exposed " << text << "\n";
        assert(false && "protected context exposed personal data");
      }
    }

    // The public-data-only short-input guard still applies.
    Expect(protected_context.description,
           RunWithUpstream(filter, harness, "he", {"合并为", "和", "tail"}, 4),
           {"和", "tail"});

    // Nothing is learned, and no native selection event is published.
    EmitCommit(harness, "richang", "日长");
    EmitKey(harness, XK_BackSpace);
    EmitKey(harness, XK_BackSpace);
    EmitCommit(harness, "richang", "日常");
    Expect(protected_context.description,
           RunWithUpstream(filter, harness, "richang", {"日长", "日常"}, 4),
           {"日长", "日常"});
    yunpin::NativeSelectionEvent event;
    assert(!yunpin::NativeSelectionEventQueue::Instance().TryPop(&event) &&
           "protected context published a native selection event");
  }
  DrainNativeSelectionEvents();
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
  TestExpressionConfigCannotArmActions();
  TestExpressionConfigCannotArmActionsWithShortUpstream();
  TestCommandLikeOrdinaryTextStaysOrdinary();
  TestShortPinyinSuppressesOnlyLongPureCjkUpstream();
  TestLongerInputAndPrivateDedupStayUnchanged();
  TestLongCorrectionGuardIsConservativeAndPageBounded();
  TestSessionCorrectionReranksBoundedUpstreamWindow();
  TestPersistentRimeHomophoneOrderOverridesStaleSnapshotOrder();
  TestPersistedCorrectionOverridesStaleUpstreamOrderAfterRestart();
  TestShippedOverlayLearnsSelectedYunPinHomophoneImmediately();
  TestSessionBridgeFailsClosedOnUnprovenDeletion();
  TestLearningCallbacksDoNotOutliveFilter();
  TestNestedNotifierCanDestroyFilter();
  TestRapidSessionLearningChurn();
  TestProtectedContextsPublishNoNativeEvents();
  TestReplayCaptureIsExplicitAndUsesTheVisibleCandidatePage();
  TestReplayCommitCarriesTheSelectedVisibleRank();
  TestMissingSnapshotKeepsActionsOffAndShortGuardAvailable();
  TestPrivateSwitchDoesNotDisableShortGuard();
  TestBothFeatureSwitchesOffStayInactive();
  TestProtectedContextsExposeNoPersonalDataButKeepPublicGuards();
  TestPrivateCandidatesStayBounded();

  std::filesystem::remove_all(user_data_dir);
  return 0;
}
