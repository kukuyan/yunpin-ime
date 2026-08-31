// SPDX-License-Identifier: GPL-3.0-only
#include <rime_api.h>

#include <iostream>
#include <string>
#include <vector>

namespace {

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

bool Probe(RimeApi* api,
           RimeSessionId session,
           const char* input,
           bool verbose) {
  api->clear_composition(session);
  if (!api->simulate_key_sequence(session, input)) {
    std::cerr << input << ": key simulation failed\n";
    return false;
  }

  RIME_STRUCT(RimeContext, context);
  if (!api->get_context(session, &context)) {
    std::cerr << input << ": context unavailable\n";
    return false;
  }

  bool has_han = false;
  if (verbose) {
    std::cout << input << ':';
  }
  for (int i = 0; i < context.menu.num_candidates; ++i) {
    const char* text = context.menu.candidates[i].text;
    if (!text) {
      continue;
    }
    const std::string candidate(text);
    has_han = has_han || IsHan(candidate);
    if (verbose) {
      std::cout << " [" << candidate << ']';
    }
  }
  if (verbose) {
    std::cout << " han=" << (has_han ? "yes" : "no") << std::endl;
  }
  api->free_context(&context);
  return has_han;
}

}  // namespace

int main(int argc, char** argv) {
  if (argc != 3) {
    std::cerr << "usage: rime_public_candidate_probe SHARED_DIR USER_DIR\n";
    return 64;
  }

  RimeApi* api = rime_get_api();
  RIME_STRUCT(RimeTraits, traits);
  traits.shared_data_dir = argv[1];
  traits.user_data_dir = argv[2];
  traits.distribution_name = "YunPin public candidate probe";
  traits.distribution_code_name = "yunpin_public_candidate_probe";
  traits.distribution_version = "1";
  traits.app_name = "rime.yunpin_public_candidate_probe";
  traits.min_log_level = 2;
  traits.log_dir = "";

  api->setup(&traits);
  api->initialize(&traits);
  if (!api->find_module("octagram") || !api->find_module("grammar")) {
    std::cerr << "octagram or grammar module is not registered\n";
    api->finalize();
    return 1;
  }
  std::cout << "octagram_modules=registered" << std::endl;
  if (api->start_maintenance(True)) {
    api->join_maintenance_thread();
  }

  const RimeSessionId session = api->create_session();
  if (!session || !api->select_schema(session, "rime_ice")) {
    std::cerr << "failed to create rime_ice session\n";
    api->finalize();
    return 1;
  }

  bool ok = true;
  for (const char* input : {"s", "sh", "shu", "shuru", "ceshi",
                            "wendingxing"}) {
    ok = Probe(api, session, input, true) && ok;
  }

  api->destroy_session(session);

  constexpr int kLifecycleSessions = 128;
  for (int cycle = 1; cycle < kLifecycleSessions; ++cycle) {
    const RimeSessionId lifecycle_session = api->create_session();
    if (!lifecycle_session ||
        !api->select_schema(lifecycle_session, "rime_ice") ||
        !Probe(api, lifecycle_session, "s", false)) {
      std::cerr << "failed public candidate probe in lifecycle cycle " << cycle
                << '\n';
      ok = false;
      if (lifecycle_session) {
        api->destroy_session(lifecycle_session);
      }
      break;
    }
    api->destroy_session(lifecycle_session);
  }
  std::cout << "lifecycle_sessions=" << kLifecycleSessions << std::endl;
  api->finalize();
  return ok ? 0 : 1;
}
