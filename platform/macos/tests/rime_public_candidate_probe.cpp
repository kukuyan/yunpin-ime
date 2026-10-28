// SPDX-License-Identifier: GPL-3.0-only
#include <rime_api.h>

#include <mach/mach.h>
#include <sys/resource.h>

#include <algorithm>
#include <array>
#include <chrono>
#include <cstdint>
#include <iostream>
#include <string>
#include <vector>

namespace {

using Clock = std::chrono::steady_clock;

struct HoldoutCase {
  const char* id;
  const char* input;
  const char* target;
  const char* accepted_alternative;
  const char* model_first;
  const char* baseline_first;
};

// Public, frozen before the final model choice. The expected first-candidate
// streams bind the A/B comparison to one packaged runtime and expose whether
// the grammar model actually participates.
constexpr std::array<HoldoutCase, 20> kHoldoutCases = {{
    {"accept_origin_image", "youyuantuma", "有原图吗", "", "有原图吗",
     "有原图吗"},
    {"accept_semantic_account", "youceshizhanghaoma", "有测试账号吗",
     "右侧是账号吗", "右侧是账号吗", "有测试账号吗"},
    {"accept_database_version", "shujukushiyongdeshinagebanben",
     "数据库使用的是哪个版本", "", "数据库使用的是哪个版本",
     "数据库使用的是哪个版本"},
    {"short_weather", "jintiantianqihenhao", "今天天气很好", "",
     "今天天气很好", "今天天气很好"},
    {"short_availability", "qingwenyoukongma", "请问有空吗", "",
     "请问有空吗", "请问有空吗"},
    {"short_how_to", "zhegeshizenmeyongde", "这个是怎么用的", "",
     "这个是怎么用的", "这个是怎么用的"},
    {"homophone_retry", "qingzaishiyici", "请再试一次", "", "请再试一次",
     "请再试一次"},
    {"homophone_usage", "shiyongfangfa", "使用方法", "", "使用方法",
     "使用方法"},
    {"homophone_which", "yinggaishinage", "应该是哪个", "", "应该是那个",
     "应该是那个"},
    {"long_email", "qingbaowenjianfadaowodeyouxiang",
     "请把文件发到我的邮箱", "", "情报文件发到我的邮箱",
     "情报文件发到我的邮箱"},
    {"long_code", "zhegedaimaweishenmewufayunxing",
     "这个代码为什么无法运行", "", "这个代码为什么无法运行",
     "这个代码为什么无法运行"},
    {"long_meeting", "qingquerenhuiyishijianhedidian",
     "请确认会议时间和地点", "", "请确认会议时间和地点",
     "请确认会议时间和地点"},
    {"circle_zero", "erlingyilingnianfabu", "二〇一〇年发布", "",
     "二〇一〇年发布", "二〇一〇年发布"},
    {"ordinary_zero", "lingduyixia", "零度以下", "", "零度以下",
     "零度以下"},
    {"heldout_tomorrow", "womenmingtianjian", "我们明天见", "",
     "我们明天见", "我们明天见"},
    {"heldout_feedback", "qingjishifankui", "请及时反馈", "", "请及时反馈",
     "请及时反馈"},
    {"heldout_network", "wangluolianjiezhengchang", "网络连接正常", "",
     "网络连接正常", "网络连接正常"},
    {"heldout_open_file", "zhegewenjianzenmedakai", "这个文件怎么打开",
     "", "这个文件怎么打开", "这个文件怎么打开"},
    {"heldout_send_address", "qingbadizhifageiwo", "请把地址发给我", "",
     "请把地址发给我", "请把地址发给我"},
    {"heldout_received", "woyijingshoudaole", "我已经收到了", "",
     "我已经收到了", "我已经受到了"},
}};

std::int64_t ElapsedMicroseconds(Clock::time_point started,
                                 Clock::time_point finished) {
  return std::chrono::duration_cast<std::chrono::microseconds>(finished -
                                                               started)
      .count();
}

std::uint64_t ResidentBytes() {
  mach_task_basic_info_data_t info = {};
  mach_msg_type_number_t count = MACH_TASK_BASIC_INFO_COUNT;
  if (task_info(mach_task_self(), MACH_TASK_BASIC_INFO,
                reinterpret_cast<task_info_t>(&info), &count) !=
      KERN_SUCCESS) {
    return 0;
  }
  return static_cast<std::uint64_t>(info.resident_size);
}

std::uint64_t MaxResidentBytes() {
  rusage usage = {};
  return getrusage(RUSAGE_SELF, &usage) == 0
             ? static_cast<std::uint64_t>(usage.ru_maxrss)
             : 0;
}

bool IsHan(const std::string& text) {
  for (std::size_t i = 0; i + 2 < text.size(); ++i) {
    const unsigned char a = static_cast<unsigned char>(text[i]);
    const unsigned char b = static_cast<unsigned char>(text[i + 1]);
    const unsigned char c = static_cast<unsigned char>(text[i + 2]);
    if (a >= 0xe3 && a <= 0xef && (b & 0xc0) == 0x80 &&
        (c & 0xc0) == 0x80) {
      return true;
    }
  }
  return false;
}

bool ProbeCandidates(RimeApi* api,
                     RimeSessionId session,
                     const char* input,
                     bool verbose) {
  api->clear_composition(session);
  if (!api->simulate_key_sequence(session, input)) {
    return false;
  }
  RIME_STRUCT(RimeContext, context);
  if (!api->get_context(session, &context)) {
    return false;
  }
  bool has_han = false;
  if (verbose) {
    std::cout << input << ':';
  }
  for (int index = 0; index < context.menu.num_candidates; ++index) {
    const char* text = context.menu.candidates[index].text;
    if (text) {
      has_han = has_han || IsHan(text);
      if (verbose) {
        std::cout << " [" << text << ']';
      }
    }
  }
  if (verbose) {
    std::cout << " han=" << (has_han ? "yes" : "no") << '\n';
  }
  api->free_context(&context);
  return has_han;
}

std::string FirstCandidate(RimeApi* api,
                           RimeSessionId session,
                           const char* input,
                           std::int64_t* elapsed_microseconds) {
  api->clear_composition(session);
  const auto started = Clock::now();
  if (!api->simulate_key_sequence(session, input)) {
    return {};
  }
  RIME_STRUCT(RimeContext, context);
  if (!api->get_context(session, &context)) {
    return {};
  }
  std::string first;
  for (int index = 0; index < context.menu.num_candidates; ++index) {
    const char* text = context.menu.candidates[index].text;
    if (index == 0 && text) {
      first = text;
    }
    if (text) {
      volatile std::size_t candidate_size = std::string(text).size();
      (void)candidate_size;
    }
  }
  api->free_context(&context);
  *elapsed_microseconds = ElapsedMicroseconds(started, Clock::now());
  return first;
}

bool BenchmarkFinalKeys(RimeApi* api,
                        RimeSessionId session,
                        std::vector<std::int64_t>* samples) {
  constexpr int kWarmupsPerCase = 5;
  constexpr int kSamplesPerCase = 20;
  for (const auto& item : kHoldoutCases) {
    const std::string input(item.input);
    const std::string prefix = input.substr(0, input.size() - 1);
    const int final_key = static_cast<unsigned char>(input.back());
    for (int iteration = 0;
         iteration < kWarmupsPerCase + kSamplesPerCase; ++iteration) {
      api->clear_composition(session);
      if (!api->simulate_key_sequence(session, prefix.c_str())) {
        return false;
      }
      const auto started = Clock::now();
      if (!api->process_key(session, final_key, 0)) {
        return false;
      }
      RIME_STRUCT(RimeContext, context);
      if (!api->get_context(session, &context) ||
          context.menu.num_candidates == 0) {
        return false;
      }
      for (int index = 0; index < context.menu.num_candidates; ++index) {
        const char* text = context.menu.candidates[index].text;
        if (text) {
          volatile std::size_t candidate_size = std::string(text).size();
          (void)candidate_size;
        }
      }
      api->free_context(&context);
      if (iteration >= kWarmupsPerCase) {
        samples->push_back(ElapsedMicroseconds(started, Clock::now()));
      }
    }
  }
  return true;
}

std::int64_t Percentile(const std::vector<std::int64_t>& samples,
                        std::size_t numerator,
                        std::size_t denominator) {
  const std::size_t index =
      std::min(samples.size() - 1,
               (samples.size() * numerator + denominator - 1) / denominator -
                   1);
  return samples[index];
}

constexpr const char* kSyntheticPrivateInput =
    "yunpingongcexianhuanqihao";
constexpr const char* kSyntheticPrivateExpected =
    "\xe4\xba\x91\xe6\x8b\xbc\xe5\x85\xac\xe6\xb5\x8b"
    "\xe9\xb9\x87\xe9\xb9\xae\xe4\xb8\x83\xe5\x8f\xb7";

bool CandidatePageContains(RimeApi* api,
                           RimeSessionId session,
                           const char* input,
                           const char* expected,
                           bool* found) {
  *found = false;
  api->clear_composition(session);
  if (!api->simulate_key_sequence(session, input)) {
    return false;
  }
  RIME_STRUCT(RimeContext, context);
  if (!api->get_context(session, &context)) {
    return false;
  }
  for (int index = 0; index < context.menu.num_candidates; ++index) {
    const char* text = context.menu.candidates[index].text;
    *found = *found || (text && std::string(text) == expected);
  }
  api->free_context(&context);
  return true;
}

// The fixture is private to the isolated probe user directory.  Use an
// unrelated synthetic long phrase so the frozen language-model holdouts stay
// public and unchanged.
constexpr const char* kSyntheticPinnedPrefix = "shuju";
constexpr const char* kSyntheticOrdinaryData = "数据";
constexpr const char* kSyntheticPinnedData = "数据平台测试候选";

bool ProbeSyntheticPrivateFixture(RimeApi* api, RimeSessionId session) {
  api->set_option(session, "yunpin_learning_allowed", True);
  bool found = false;
  if (!CandidatePageContains(api, session, kSyntheticPrivateInput,
                             kSyntheticPrivateExpected, &found) || !found) {
    std::cerr << "synthetic private fixture was not visible\n";
    return false;
  }
  api->clear_composition(session);
  if (!api->simulate_key_sequence(session, kSyntheticPinnedPrefix)) {
    std::cerr << "failed to compose synthetic pinned prefix\n";
    return false;
  }
  RIME_STRUCT(RimeContext, context);
  if (!api->get_context(session, &context)) {
    return false;
  }
  const bool ordered = context.menu.num_candidates >= 2 &&
      context.menu.candidates[0].text && context.menu.candidates[1].text &&
      std::string(context.menu.candidates[0].text) == kSyntheticOrdinaryData &&
      std::string(context.menu.candidates[1].text) == kSyntheticPinnedData;
  api->free_context(&context);
  if (!ordered || !api->select_candidate(session, 1)) {
    std::cerr << "ordinary/pinned candidate order or selection failed\n";
    return false;
  }
  RIME_STRUCT(RimeCommit, commit);
  if (!api->get_commit(session, &commit)) {
    std::cerr << "pinned candidate did not commit\n";
    return false;
  }
  const bool committed = commit.text &&
      std::string(commit.text) == kSyntheticPinnedData;
  api->free_commit(&commit);
  if (!committed) {
    std::cerr << "pinned candidate committed unexpected text\n";
    return false;
  }
  std::cout << "synthetic_private_fixture=pass\n";
  return true;
}

bool ProbeSyntheticPrivateCounterfactual(RimeApi* api,
                                         RimeSessionId session) {
  api->set_option(session, "yunpin_learning_allowed", True);
  bool found = false;
  if (!CandidatePageContains(api, session, kSyntheticPrivateInput,
                             kSyntheticPrivateExpected, &found)) {
    std::cerr << "synthetic private counterfactual could not enumerate\n";
    return false;
  }
  if (found) {
    std::cerr << "synthetic private fixture remained in the candidate page\n";
    return false;
  }
  std::cout << "synthetic_private_counterfactual=pass\n";
  return true;
}

}  // namespace

int main(int argc, char** argv) {
  if (argc != 4) {
    std::cerr << "usage: rime_public_candidate_probe "
                 "SHARED_DIR USER_DIR "
                 "prepare-model|prepare-baseline|model|baseline|private-off\n";
    return 64;
  }
  const std::string mode(argv[3]);
  const bool prepare_mode =
      mode == "prepare-model" || mode == "prepare-baseline";
  const bool measurement_mode = mode == "model" || mode == "baseline";
  const bool private_off_mode = mode == "private-off";
  if (!prepare_mode && !measurement_mode && !private_off_mode) {
    std::cerr << "invalid probe mode\n";
    return 64;
  }
  const bool model_mode = mode == "model" || mode == "prepare-model" ||
                          mode == "private-off";
  const auto process_started = Clock::now();

  RimeApi* api = rime_get_api();
  RIME_STRUCT(RimeTraits, traits);
  traits.shared_data_dir = argv[1];
  traits.user_data_dir = argv[2];
  traits.distribution_name = "YunPin public candidate probe";
  traits.distribution_code_name = "yunpin_public_candidate_probe";
  traits.distribution_version = "1";
  traits.app_name = "rime.yunpin_public_candidate_probe";
  // INFO is captured only in the wrapper's private temporary stderr so the
  // exact reviewed model file-open can be placed between schema markers.
  traits.min_log_level = 0;
  traits.log_dir = "";

  const auto initialize_started = Clock::now();
  api->setup(&traits);
  api->initialize(&traits);
  const auto initialize_finished = Clock::now();
  const std::uint64_t rss_after_initialize = ResidentBytes();
  if (!api->find_module("octagram") || !api->find_module("grammar")) {
    std::cerr << "octagram or grammar module is not registered\n";
    api->finalize();
    return 1;
  }
  std::cout << "mode=" << mode << '\n'
            << "octagram_modules=registered\n";

  if (prepare_mode) {
    const bool maintenance_started = api->start_maintenance(True);
    if (maintenance_started) {
      api->join_maintenance_thread();
    }
    const std::uint64_t peak_rss = MaxResidentBytes();
    const bool ok = maintenance_started && rss_after_initialize > 0 &&
                    peak_rss > 0;
    std::cout
        << "cache_condition=isolated-deployment-process-os-warm\n"
        << "deployment_phase_elapsed_us="
        << ElapsedMicroseconds(process_started, Clock::now()) << '\n'
        << "deployment_phase_peak_rss_bytes=" << peak_rss << '\n'
        << "deployment_pass=" << (ok ? "true" : "false") << std::endl;
    api->finalize();
    return ok ? 0 : 1;
  }

  const auto schema_started = Clock::now();
  std::cerr << "schema_select_begin\n";
  const RimeSessionId session = api->create_session();
  if (!session || !api->select_schema(session, "rime_ice")) {
    std::cerr << "failed to create rime_ice session\n";
    api->finalize();
    return 1;
  }
  std::cerr << "schema_select_end\n";
  const auto schema_finished = Clock::now();
  const std::uint64_t rss_after_schema_select = ResidentBytes();

  if (private_off_mode) {
    const bool ok = ProbeSyntheticPrivateCounterfactual(api, session);
    api->destroy_session(session);
    api->finalize();
    return ok ? 0 : 1;
  }

  bool ok = rss_after_initialize > 0 && rss_after_schema_select > 0;
  int accepted_quality_cases = 0;
  std::int64_t first_complete_input_us = 0;
  std::uint64_t rss_after_first_input = 0;
  for (std::size_t index = 0; index < kHoldoutCases.size(); ++index) {
    const auto& item = kHoldoutCases[index];
    std::int64_t elapsed = 0;
    const std::string first =
        FirstCandidate(api, session, item.input, &elapsed);
    const char* expected_first =
        model_mode ? item.model_first : item.baseline_first;
    const bool stream_matches = first == expected_first;
    const bool target_matches =
        first == item.target ||
        (item.accepted_alternative[0] != '\0' &&
         first == item.accepted_alternative);
    accepted_quality_cases += target_matches ? 1 : 0;
    ok = stream_matches && ok;
    std::cout << "holdout_case=" << item.id << ':'
              << (stream_matches ? "pass" : "fail") << '\n';
    if (model_mode &&
        (index == 0 || index == 1 || index == 2 || index == 6 ||
         index == 19)) {
      std::cout << "grammar_quality=" << item.input << ':' << first << '\n';
    }
    if (index == 0) {
      first_complete_input_us = elapsed;
      rss_after_first_input = ResidentBytes();
    }
  }
  const std::uint64_t rss_after_holdout = ResidentBytes();
  const int expected_quality_cases = model_mode ? 18 : 17;
  ok = accepted_quality_cases == expected_quality_cases && ok;

  std::vector<std::int64_t> samples;
  samples.reserve(kHoldoutCases.size() * 20);
  if (!BenchmarkFinalKeys(api, session, &samples) || samples.empty()) {
    ok = false;
  }
  std::int64_t p95 = 0;
  if (!samples.empty()) {
    std::sort(samples.begin(), samples.end());
    p95 = Percentile(samples, 95, 100);
  }
  constexpr std::int64_t kP95GateMicroseconds = 20000;
  ok = p95 > 0 && p95 <= kP95GateMicroseconds && ok;
  // Snapshot the peak after the identical public quality workload on both
  // sides of the A/B. Model-only private/lifecycle checks below must not
  // contaminate the resident-runtime comparison.
  const std::uint64_t measurement_max_rss = MaxResidentBytes();
  const std::int64_t measurement_process_elapsed_us =
      ElapsedMicroseconds(process_started, Clock::now());
  ok = measurement_max_rss > 0 && ok;

  if (model_mode) {
    ok = ProbeSyntheticPrivateFixture(api, session) && ok;
    for (const char* input : {"s", "sh", "shu", "shuru", "ceshi",
                              "wendingxing"}) {
      ok = ProbeCandidates(api, session, input, true) && ok;
    }
  }

  api->destroy_session(session);
  constexpr int kLifecycleSessions = 128;
  if (model_mode) {
    for (int cycle = 1; cycle < kLifecycleSessions; ++cycle) {
      const RimeSessionId lifecycle_session = api->create_session();
      if (!lifecycle_session ||
          !api->select_schema(lifecycle_session, "rime_ice") ||
          !ProbeCandidates(api, lifecycle_session, "s", false)) {
        ok = false;
        if (lifecycle_session) {
          api->destroy_session(lifecycle_session);
        }
        break;
      }
      api->destroy_session(lifecycle_session);
    }
    std::cout << "lifecycle_sessions=" << kLifecycleSessions << '\n';
  }

  ok = rss_after_first_input > 0 && rss_after_holdout > 0 && ok;
  std::cout
      << "cache_condition=process-cold-deployed-user-data-os-warm\n"
      << "initialize_us="
      << ElapsedMicroseconds(initialize_started, initialize_finished) << '\n'
      << "schema_select_us="
      << ElapsedMicroseconds(schema_started, schema_finished) << '\n'
      << "first_complete_input_us=" << first_complete_input_us << '\n'
      << "rss_after_initialize_bytes=" << rss_after_initialize << '\n'
      << "rss_after_schema_select_bytes=" << rss_after_schema_select << '\n'
      << "rss_after_first_input_bytes=" << rss_after_first_input << '\n'
      << "rss_after_holdout_bytes=" << rss_after_holdout << '\n'
      << "measurement_max_rss_bytes=" << measurement_max_rss << '\n'
      << "accepted_quality_cases=" << accepted_quality_cases << '\n'
      << "holdout_case_count=" << kHoldoutCases.size() << '\n'
      << "final_key_candidate_p95_us=" << p95 << '\n'
      << "measurement_process_elapsed_us="
      << measurement_process_elapsed_us << '\n'
      << "grammar_quality_pass=" << (ok ? "true" : "false") << std::endl;
  api->finalize();
  return ok ? 0 : 1;
}
