// SPDX-License-Identifier: GPL-3.0-only
#include <rime_api.h>

#include <algorithm>
#include <array>
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

  std::vector<std::string> capped_candidates;
  if (!Compose(api, session, "zgsh", &capped_candidates) ||
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

  api->set_option(session, "yunpin_private_mode", True);
  std::vector<std::string> private_candidates;
  if (!Compose(api, session, "zgsh", &private_candidates) ||
      CountPrivatePhrases(private_candidates) != 0) {
    std::cerr << "private mode did not suppress the YunPin fixture\n";
    ok = false;
  }

  api->destroy_session(session);
  api->finalize();
  if (!ok) {
    return 1;
  }
  std::cout
      << "verified merged YunPin ranking, quota, deduplication, commit, and "
         "private-mode suppression\n";
  return 0;
}
