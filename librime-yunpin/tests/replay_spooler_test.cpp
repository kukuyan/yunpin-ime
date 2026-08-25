// SPDX-License-Identifier: Apache-2.0
#include "yunpin/replay_native.hpp"

#include <chrono>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <stdexcept>
#include <string>
#include <thread>

namespace {

namespace fs = std::filesystem;

constexpr char kSessionId[] = "11111111111111111111111111111111";

void Check(bool condition, const char* message) {
  if (!condition) {
    throw std::runtime_error(message);
  }
}

void WriteActive(const fs::path& root, std::string_view state) {
  fs::create_directories(root);
  std::ofstream output(root / "active.json", std::ios::trunc | std::ios::binary);
  output << "{\n"
            "  \"version\": \"yunpin.replay.session.v1\",\n"
            "  \"session_id\": \""
         << kSessionId << "\",\n"
         << "  \"state\": \"" << state << "\"\n"
         << "}\n";
  output.close();
#if !defined(_WIN32)
  fs::permissions(root / "active.json", fs::perms::owner_read |
                                           fs::perms::owner_write,
                  fs::perm_options::replace);
#endif
}

yunpin::ReplayNativeEvent SyntheticComposition() {
  yunpin::ReplayNativeEvent event;
  event.type = yunpin::ReplayEventType::kComposition;
  event.monotonic_us = 10;
  event.utc_unix_us = 20;
  yunpin::CopyReplayText(&event.composition.raw_input, "synthetic");
  yunpin::CopyReplayText(&event.composition.normalized_pinyin, "synthetic");
  event.composition.caret_byte = event.composition.raw_input.size;
  event.composition.exact_path_available = true;
  event.composition.candidate_count = 2;
  yunpin::CopyReplayText(&event.composition.candidates[0].text,
                         "synthetic correction");
  event.composition.candidates[0].is_correction = true;
  event.composition.candidates[0].highlighted = true;
  yunpin::CopyReplayText(&event.composition.candidates[1].text,
                         "synthetic exact");
  return event;
}

template <typename Predicate>
bool WaitUntil(Predicate predicate) {
  for (int attempt = 0; attempt < 100; ++attempt) {
    if (predicate()) {
      return true;
    }
    std::this_thread::sleep_for(std::chrono::milliseconds(20));
  }
  return false;
}

void TestExplicitStartControlsBackgroundSpool() {
  const auto unique = std::to_string(
      std::chrono::steady_clock::now().time_since_epoch().count());
  const fs::path base =
      fs::temp_directory_path() / ("yunpin-replay-spooler-" + unique);
  const fs::path root = base / "YunPin" / "ReplayLab";
  const fs::path output =
      root / "native" / (std::string(kSessionId) + ".native.yunpinreplay");
  fs::remove_all(base);
  WriteActive(root, "paused");

  const std::string root_utf8 = root.u8string();
  Check(YunPinStartReplaySpoolerV1(root_utf8.c_str()),
        "replay spooler did not start");
  auto& producer = yunpin::GlobalReplayNativeProducer();
  Check(!producer.enabled(), "paused lab enabled native capture");
  Check(!producer.TryPush(SyntheticComposition()),
        "paused lab accepted native text");

  WriteActive(root, "running");
  Check(WaitUntil([&] { return producer.enabled(); }),
        "running lab did not enable producer");
  Check(producer.TryPush(SyntheticComposition()),
        "running lab rejected synthetic callback");
  Check(WaitUntil([&] {
          std::error_code error;
          return fs::is_regular_file(output, error) &&
                 fs::file_size(output, error) > 0;
        }),
        "background sink did not persist the native callback");

  WriteActive(root, "paused");
  Check(WaitUntil([&] { return !producer.enabled(); }),
        "pause did not disable the producer");
  Check(!producer.TryPush(SyntheticComposition()),
        "paused producer accepted a later callback");
  YunPinStopReplaySpoolerV1();

  std::string recorded;
  {
    std::ifstream input(output, std::ios::binary);
    Check(input.is_open(), "native replay trace could not be reopened");
    recorded.assign(std::istreambuf_iterator<char>(input),
                    std::istreambuf_iterator<char>());
    Check(!input.bad(), "native replay trace could not be read completely");
  }
  Check(recorded.find("\"type\":\"composition_snapshot\"") !=
            std::string::npos,
        "native spool omitted the composition frame");
  Check(recorded.find("\"is_correction\":true") != std::string::npos,
        "native spool lost the correction marker");
  fs::remove_all(base);
}

void EmitSyntheticHostSession(const fs::path& root,
                              std::string_view session_id) {
  Check(session_id.size() == 32, "invalid external Replay Lab session id");
  const fs::path output =
      root / "native" /
      (std::string(session_id) + ".native.yunpinreplay");
  const std::string root_utf8 = root.u8string();
  Check(YunPinStartReplaySpoolerV1(root_utf8.c_str()),
        "external replay spooler did not start");
  auto& producer = yunpin::GlobalReplayNativeProducer();
  Check(WaitUntil([&] { return producer.enabled(); }),
        "external running lab did not enable producer");
  Check(producer.TryPush(SyntheticComposition()),
        "external synthetic host callback was rejected");
  Check(WaitUntil([&] {
          std::error_code error;
          return fs::is_regular_file(output, error) &&
                 fs::file_size(output, error) > 0;
        }),
        "external synthetic host callback was not persisted");
  YunPinStopReplaySpoolerV1();
}

}  // namespace

int main(int argc, char** argv) {
  try {
    if (argc == 4 && std::string_view(argv[1]) == "--emit-running-session") {
      EmitSyntheticHostSession(fs::u8path(argv[2]), argv[3]);
      std::cout << "synthetic Replay Lab host session emitted\n";
      return 0;
    }
    Check(argc == 1,
          "usage: replay_spooler_test [--emit-running-session ROOT SESSION]");
    TestExplicitStartControlsBackgroundSpool();
    std::cout << "replay spooler tests passed\n";
    return 0;
  } catch (const std::exception& error) {
    YunPinStopReplaySpoolerV1();
    std::cerr << error.what() << '\n';
    return 1;
  }
}
