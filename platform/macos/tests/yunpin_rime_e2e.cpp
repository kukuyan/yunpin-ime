// SPDX-License-Identifier: GPL-3.0-only
#include <rime_api.h>

#include <algorithm>
#include <array>
#include <chrono>
#include <cstdint>
#include <cstring>
#include <iostream>
#include <string>
#include <vector>

namespace {

constexpr const char* kExpected =
    "中国石化销售股份有限公司河北石家庄石油分公司";
constexpr std::array<const char*, 3> kPrivatePhrases = {
    kExpected,
    "中国石化工程建设有限公司",
    "中国石化科技发展有限公司",
};

std::vector<std::string> Candidates(RimeApi* api, RimeSessionId session) {
  RIME_STRUCT(RimeContext, context);
  if (!api->get_context(session, &context)) {
    return {};
  }
  std::vector<std::string> result;
  for (int i = 0; i < context.menu.num_candidates; ++i) {
    if (context.menu.candidates[i].text) {
      result.emplace_back(context.menu.candidates[i].text);
    }
  }
  api->free_context(&context);
  return result;
}

std::string CandidateComment(RimeApi* api,
                             RimeSessionId session,
                             const std::string& text) {
  RIME_STRUCT(RimeContext, context);
  if (!api->get_context(session, &context)) {
    return {};
  }
  std::string result;
  for (int index = 0; index < context.menu.num_candidates; ++index) {
    const RimeCandidate& candidate = context.menu.candidates[index];
    if (candidate.text && text == candidate.text && candidate.comment) {
      result = candidate.comment;
      break;
    }
  }
  api->free_context(&context);
  return result;
}

bool Compose(RimeApi* api,
             RimeSessionId session,
             const char* input,
             std::vector<std::string>* candidates) {
  api->clear_composition(session);
  if (!api->simulate_key_sequence(session, input)) {
    std::cerr << "failed to simulate fixture input\n";
    return false;
  }
  *candidates = Candidates(api, session);
  return true;
}

bool ExpectFirst(RimeApi* api, RimeSessionId session, const char* input) {
  std::vector<std::string> candidates;
  if (!Compose(api, session, input, &candidates)) {
    return false;
  }
  if (candidates.empty() || candidates.front() != kExpected) {
    std::cerr << "YunPin fixture phrase was not the first candidate\n";
    return false;
  }
  return true;
}

std::size_t CountPrivatePhrases(const std::vector<std::string>& candidates) {
  return std::count_if(candidates.begin(), candidates.end(),
                       [](const std::string& candidate) {
                         return std::find(kPrivatePhrases.begin(),
                                          kPrivatePhrases.end(),
                                          candidate) != kPrivatePhrases.end();
                       });
}

bool Contains(const std::vector<std::string>& candidates,
              const std::string& text) {
  return std::find(candidates.begin(), candidates.end(), text) !=
         candidates.end();
}

void PrintCandidates(const std::vector<std::string>& candidates) {
  for (const auto& candidate : candidates) {
    std::cerr << " [" << candidate << ']';
  }
  std::cerr << '\n';
}

bool ExpectCommit(RimeApi* api, RimeSessionId session, const char* input) {
  std::vector<std::string> candidates;
  if (!Compose(api, session, input, &candidates) || candidates.empty() ||
      candidates.front() != kExpected || !api->select_candidate(session, 0)) {
    std::cerr << "failed to select the first YunPin candidate\n";
    return false;
  }
  RIME_STRUCT(RimeCommit, commit);
  if (!api->get_commit(session, &commit)) {
    std::cerr << "selected YunPin candidate did not commit\n";
    return false;
  }
  const bool matches = commit.text && std::string(commit.text) == kExpected;
  api->free_commit(&commit);
  if (!matches) {
    std::cerr << "YunPin commit text did not match the selected candidate\n";
  }
  return matches;
}

bool ExpectCandidateFirst(RimeApi* api,
                          RimeSessionId session,
                          const char* input,
                          const char* expected,
                          const char* correction_hint = nullptr) {
  std::vector<std::string> candidates;
  if (!Compose(api, session, input, &candidates) || candidates.empty() ||
      candidates.front() != expected) {
    std::cerr << "unexpected first candidate for " << input << ':';
    PrintCandidates(candidates);
    return false;
  }
  if (correction_hint) {
    const std::string comment = CandidateComment(api, session, expected);
    if (comment.find(correction_hint) == std::string::npos) {
      std::cerr << "corrected candidate for " << input
                << " did not expose its canonical spelling; comment=["
                << comment << "]\n";
      return false;
    }
  }
  return true;
}

bool ExpectCandidateAtRank(RimeApi* api,
                           RimeSessionId session,
                           const char* input,
                           const char* expected,
                           std::size_t expected_rank,
                           const char* correction_hint = nullptr) {
  std::vector<std::string> candidates;
  if (!Compose(api, session, input, &candidates) || expected_rank == 0 ||
      candidates.size() < expected_rank ||
      candidates[expected_rank - 1] != expected) {
    std::cerr << "unexpected rank for " << expected << " after " << input
              << "; wanted #" << expected_rank << ':';
    PrintCandidates(candidates);
    return false;
  }
  if (correction_hint) {
    const std::string comment = CandidateComment(api, session, expected);
    if (comment.find(correction_hint) == std::string::npos) {
      std::cerr << "corrected candidate for " << input
                << " did not expose its canonical spelling; comment=["
                << comment << "]\n";
      return false;
    }
  }
  return true;
}

bool ExpectCandidateAbsent(RimeApi* api,
                           RimeSessionId session,
                           const char* input,
                           const char* unexpected) {
  std::vector<std::string> candidates;
  if (!Compose(api, session, input, &candidates)) {
    return false;
  }
  if (Contains(candidates, unexpected)) {
    std::cerr << "unexpected speculative candidate for " << input << ':';
    PrintCandidates(candidates);
    return false;
  }
  return true;
}

bool ExpectCandidatesPresent(RimeApi* api,
                             RimeSessionId session,
                             const char* input,
                             const std::vector<std::string>& expected) {
  std::vector<std::string> candidates;
  if (!Compose(api, session, input, &candidates)) {
    return false;
  }
  for (const auto& text : expected) {
    if (!Contains(candidates, text)) {
      std::cerr << "missing exact homophone candidate " << text << " for "
                << input << ':';
      PrintCandidates(candidates);
      return false;
    }
  }
  return true;
}

bool ExpectCandidatePinyinToggle(RimeApi* api, RimeSessionId session) {
  constexpr const char* kInput = "shangban";
  constexpr const char* kCandidate = "上班";
  std::vector<std::string> default_off;
  if (!Compose(api, session, kInput, &default_off) || default_off.empty() ||
      default_off.front() != kCandidate ||
      !CandidateComment(api, session, kCandidate).empty()) {
    std::cerr << "candidate Pinyin was not hidden by default\n";
    return false;
  }

  api->set_option(session, "yunpin_show_candidate_pinyin", True);
  std::vector<std::string> visible;
  if (!Compose(api, session, kInput, &visible) || visible != default_off) {
    std::cerr << "showing candidate Pinyin changed candidate order or count\n";
    return false;
  }
  const std::string visible_comment =
      CandidateComment(api, session, kCandidate);
  if (visible_comment.find("shang ban") == std::string::npos) {
    std::cerr << "candidate Pinyin did not become visible after enabling it\n";
    return false;
  }

  api->set_option(session, "yunpin_show_candidate_pinyin", False);
  std::vector<std::string> hidden_again;
  if (!Compose(api, session, kInput, &hidden_again) ||
      hidden_again != default_off ||
      !CandidateComment(api, session, kCandidate).empty()) {
    std::cerr << "candidate Pinyin did not hide again without ranking changes\n";
    return false;
  }
  return true;
}

bool SelectAndDrain(RimeApi* api,
                    RimeSessionId session,
                    const std::vector<std::string>& candidates,
                    const std::string& text) {
  const auto found = std::find(candidates.begin(), candidates.end(), text);
  if (found == candidates.end() ||
      !api->select_candidate(
          session, static_cast<int>(found - candidates.begin()))) {
    return false;
  }
  RIME_STRUCT(RimeCommit, commit);
  if (!api->get_commit(session, &commit)) {
    return false;
  }
  const bool matches = commit.text && text == commit.text;
  api->free_commit(&commit);
  return matches;
}

bool BenchmarkFinalKey(RimeApi* api,
                       RimeSessionId session,
                       const std::string& input,
                       const std::string& expected,
                       std::size_t expected_rank) {
  if (input.size() < 2) {
    return false;
  }
  const std::string prefix = input.substr(0, input.size() - 1);
  const int final_key = static_cast<unsigned char>(input.back());
  constexpr std::size_t kWarmups = 10;
  constexpr std::size_t kSamples = 100;
  std::vector<std::int64_t> microseconds;
  microseconds.reserve(kSamples);
  for (std::size_t iteration = 0;
       iteration < kWarmups + kSamples; ++iteration) {
    api->clear_composition(session);
    if (!api->simulate_key_sequence(session, prefix.c_str())) {
      return false;
    }
    const auto start = std::chrono::steady_clock::now();
    if (!api->process_key(session, final_key, 0)) {
      return false;
    }
    const std::vector<std::string> candidates = Candidates(api, session);
    const auto elapsed = std::chrono::duration_cast<std::chrono::microseconds>(
        std::chrono::steady_clock::now() - start);
    if (expected_rank == 0 || candidates.size() < expected_rank ||
        candidates[expected_rank - 1] != expected) {
      return false;
    }
    if (iteration >= kWarmups) {
      microseconds.push_back(elapsed.count());
    }
  }
  std::sort(microseconds.begin(), microseconds.end());
  const std::int64_t p95 = microseconds[94];
  std::cout << "YunPin final-key P95 " << p95 << "us for "
            << input.size() << " ASCII bytes\n";
  return p95 <= 20000;
}

bool ExerciseSessionLifecycleChurn(RimeApi* api) {
  constexpr int kIterations = 128;
  for (int iteration = 0; iteration < kIterations; ++iteration) {
    const RimeSessionId session = api->create_session();
    if (!session || !api->select_schema(session, "yunpin_e2e")) {
      std::cerr << "failed to create lifecycle session #" << iteration
                << '\n';
      if (session) {
        api->destroy_session(session);
      }
      return false;
    }

    // Exercise option, update, select/commit and immediate destroy notifier
    // paths.  Intentionally do not drain the commit on alternating sessions:
    // an IMK controller can disappear immediately after selection.
    api->set_option(session, "yunpin_private_mode", True);
    api->set_option(session, "yunpin_private_mode", False);
    std::vector<std::string> candidates;
    if (!Compose(api, session, "richang", &candidates) ||
        candidates.empty() || !api->select_candidate(session, 0)) {
      std::cerr << "failed lifecycle select/commit #" << iteration << '\n';
      api->destroy_session(session);
      return false;
    }
    if ((iteration & 1) == 0) {
      RIME_STRUCT(RimeCommit, commit);
      if (api->get_commit(session, &commit)) {
        api->free_commit(&commit);
      }
    }
    if (!api->destroy_session(session)) {
      std::cerr << "failed immediate lifecycle destroy #" << iteration
                << '\n';
      return false;
    }
  }
  return true;
}

}  // namespace

int main(int argc, char** argv) {
  if (argc != 3) {
    std::cerr << "usage: yunpin_rime_e2e SHARED_DATA_DIR USER_DATA_DIR\n";
    return 2;
  }

  RimeApi* api = rime_get_api();
  RIME_STRUCT(RimeTraits, traits);
  traits.shared_data_dir = argv[1];
  traits.user_data_dir = argv[2];
  traits.distribution_name = "YunPin E2E fixture";
  traits.distribution_code_name = "yunpin_e2e";
  traits.distribution_version = "1.0";
  traits.app_name = "rime.yunpin_e2e";
  traits.min_log_level = 2;
  traits.log_dir = "";

  api->setup(&traits);
  api->initialize(&traits);
  if (!api->find_module("yunpin")) {
    std::cerr << "merged yunpin module is not registered\n";
    api->finalize();
    return 1;
  }
  if (api->start_maintenance(True)) {
    api->join_maintenance_thread();
  }

  const RimeSessionId session = api->create_session();
  if (!session || !api->select_schema(session, "yunpin_e2e")) {
    std::cerr << "failed to create fixture session/schema\n";
    api->finalize();
    return 1;
  }

  bool ok = ExpectFirst(api, session, "zgsh");
  ok = ExpectFirst(
           api, session,
           "zhongguoshihuaxiaoshougufenyouxiangongsihebeishijiazhuangshiyoufengongsi") &&
       ok;

  std::vector<std::string> short_candidates;
  if (!Compose(api, session, "he", &short_candidates) ||
      Contains(short_candidates, "合并为") ||
      !Contains(short_candidates, "和")) {
    std::cerr << "short-input guard did not remove only the long prediction\n";
    PrintCandidates(short_candidates);
    ok = false;
  }

  std::vector<std::string> initials_candidates;
  if (!Compose(api, session, "zgsh", &initials_candidates) ||
      CountPrivatePhrases(initials_candidates) != 1) {
    std::cerr << "short initials recalled a non-pinned long private phrase\n";
    PrintCandidates(initials_candidates);
    ok = false;
  }

  std::vector<std::string> capped_candidates;
  if (!Compose(api, session, "zhongguoshihua", &capped_candidates) ||
      CountPrivatePhrases(capped_candidates) != 2) {
    std::cerr << "YunPin did not enforce the two-personal-candidate page cap\n";
    ok = false;
  }

  std::vector<std::string> deduplicated_candidates;
  if (!Compose(
          api, session,
          "zhongguoshihuaxiaoshougufenyouxiangongsihebeishijiazhuangshiyoufengongsi",
          &deduplicated_candidates) ||
      std::count(deduplicated_candidates.begin(),
                 deduplicated_candidates.end(), kExpected) != 1) {
    std::cerr << "YunPin/upstream duplicate was not removed\n";
    ok = false;
  }

  ok = ExpectCommit(
           api, session,
           "zhongguoshihuaxiaoshougufenyouxiangongsihebeishijiazhuangshiyoufengongsi") &&
       ok;

  // Correct whole-input Pinyin is an absolute no-expansion boundary. These
  // high-weight distractors would win under the retired aggressive policy,
  // but a complete normal/non-correction Prism path must suppress all typo
  // searches before candidate ranking.
  ok = ExpectCandidateFirst(
           api, session, "shousubijiaokuaideshihou",
           "手速比较快的时候") &&
       ok;
  ok = ExpectCandidateFirst(
           api, session, "shouxubijiaokuaideshihou",
           "手续比较快的时候") &&
       ok;
  ok = ExpectCandidateAbsent(
           api, session, "shouxubijiaokuaideshihou",
           "手速比较快的时候") &&
       ok;
  ok = ExpectCandidateFirst(
           api, session, "shujukushiyongdeshinagebanben",
           "数据库使用的是哪个版本") &&
       ok;
  ok = ExpectCandidateAbsent(
           api, session, "shujukushiyongdeshinagebanben",
           "数目录使用的是哪个版本") &&
       ok;
  ok = ExpectCandidateFirst(
           api, session, "youjubeiyidingdejiucuolianxiangnengli",
           "有具备一定的纠错联想能力") &&
       ok;
  ok = ExpectCandidateAbsent(
           api, session, "youjubeiyidingdejiucuolianxiangnengli",
           "要具备一定的纠错联想能力") &&
       ok;
  ok = ExpectCandidateFirst(api, session, "shangban", "上班") && ok;
  ok = ExpectCandidateAbsent(api, session, "shangban", "山班") && ok;
  ok = ExpectCandidatePinyinToggle(api, session) && ok;

  // `youceshizhanghaoma` has two exact homophone parses. This fixture only
  // proves both stay ordinary dictionary candidates; choosing between them is
  // a language-model/personal-learning task, never typo-correction evidence.
  ok = ExpectCandidatesPresent(
           api, session, "youceshizhanghaoma",
           {"右侧是账号吗", "右侧市长好吗"}) &&
       ok;

  // The echo translator supplies one ordinary fail-safe candidate for this
  // synthetic fixture. A single invalid trailing key may therefore expose one
  // bridge correction at total rank #2; the filter must never promote it to
  // #1. Two invalid regions cannot be joined by one correction edge and must
  // fail closed.
  api->set_option(session, "yunpin_show_candidate_pinyin", True);
  ok = ExpectCandidateAtRank(
           api, session, "shousubijiaokuaideshihouu",
           "手速比较快的时候", 2, "shou su") &&
       ok;
  api->set_option(session, "yunpin_show_candidate_pinyin", False);
  ok = ExpectCandidateAbsent(
           api, session, "shouusubijiaokuaideshihouu",
           "手速比较快的时候") &&
       ok;
  ok = ExpectCandidateFirst(api, session, "xu", "需") && ok;
  ok = ExpectCandidateFirst(api, session, "you", "有") && ok;
  ok = BenchmarkFinalKey(api, session,
                         "shousubijiaokuaideshihouu",
                         "手速比较快的时候", 2) &&
       ok;
  ok = BenchmarkFinalKey(
           api, session, "shujukushiyongdeshinagebanben",
           "数据库使用的是哪个版本", 1) &&
       ok;

  std::vector<std::string> initial_correction_candidates;
  if (!Compose(api, session, "richang", &initial_correction_candidates) ||
      initial_correction_candidates.empty() ||
      initial_correction_candidates.front() != "日长" ||
      !SelectAndDrain(api, session, initial_correction_candidates, "日长")) {
    std::cerr << "failed to establish the correction-learning fixture\n";
    ok = false;
  }
  if (!api->simulate_key_sequence(session, "{BackSpace}{BackSpace}")) {
    std::cerr << "failed to deliver correction Backspace events\n";
    ok = false;
  }
  std::vector<std::string> replacement_candidates;
  if (!Compose(api, session, "richang", &replacement_candidates) ||
      !SelectAndDrain(api, session, replacement_candidates, "日常")) {
    std::cerr << "failed to commit the intended replacement word\n";
    ok = false;
  }
  std::vector<std::string> reranked_candidates;
  if (!Compose(api, session, "richang", &reranked_candidates)) {
    std::cerr << "failed to requery corrected pinyin\n";
    ok = false;
  } else {
    const auto right = std::find(reranked_candidates.begin(),
                                 reranked_candidates.end(), "日常");
    const auto wrong = std::find(reranked_candidates.begin(),
                                 reranked_candidates.end(), "日长");
    if (right == reranked_candidates.end() ||
        wrong == reranked_candidates.end() || right >= wrong) {
      std::cerr << "explicit correction did not rerank 日常 ahead of 日长\n";
      PrintCandidates(reranked_candidates);
      ok = false;
    }
  }

  api->set_option(session, "yunpin_private_mode", True);
  std::vector<std::string> private_candidates;
  if (!Compose(api, session, "zgsh", &private_candidates) ||
      CountPrivatePhrases(private_candidates) != 0) {
    std::cerr << "private mode did not suppress the YunPin fixture\n";
    ok = false;
  }

  api->destroy_session(session);
  ok = ExerciseSessionLifecycleChurn(api) && ok;
  api->finalize();
  if (!ok) {
    return 1;
  }
  std::cout
      << "verified conservative one-bridge correction, exact-input "
         "stability, short guard, ranking, quota, deduplication, commit, "
         "candidate-Pinyin visibility, session correction, private-mode "
         "suppression, and 128-session notifier churn\n";
  return 0;
}
