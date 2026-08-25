// SPDX-License-Identifier: Apache-2.0
#include "yunpin/replay_native.hpp"

#include <iostream>
#include <stdexcept>
#include <string>

namespace {

void Check(bool condition, const char* message) {
  if (!condition) {
    throw std::runtime_error(message);
  }
}

yunpin::ReplayNativeEvent SyntheticEvent(std::string_view raw) {
  yunpin::ReplayNativeEvent event;
  event.type = yunpin::ReplayEventType::kComposition;
  event.monotonic_us = 10;
  event.utc_unix_us = 20;
  yunpin::CopyReplayText(&event.composition.raw_input, raw);
  yunpin::CopyReplayText(&event.composition.normalized_pinyin, raw);
  event.composition.caret_byte =
      static_cast<std::uint16_t>(event.composition.raw_input.size);
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

void TestDisabledByDefaultAndBoundedDrain() {
  yunpin::ReplayNativeProducer producer;
  const auto event = SyntheticEvent("synthetic");
  Check(!producer.enabled(), "producer must default off");
  Check(!producer.TryPush(event), "disabled producer accepted an event");
  Check(producer.dropped() == 0,
        "disabled capture must not be reported as queue loss");

  producer.SetEnabled(true);
  Check(producer.TryPush(event), "enabled producer rejected an event");
  char json[yunpin::kReplayJsonLimit + 1]{};
  const std::size_t size = producer.DrainJson(json, sizeof(json));
  Check(size > 0 && size <= yunpin::kReplayJsonLimit,
        "event did not drain within JSON bound");
  const std::string encoded(json, size);
  Check(encoded.find("\"version\":\"yunpin.replay.native.v1\"") !=
            std::string::npos,
        "native version is missing");
  Check(encoded.find("\"is_correction\":true") != std::string::npos,
        "correction flag is missing");
  Check(encoded.find("\"highlighted\":true") != std::string::npos,
        "highlight flag is missing");
  Check(producer.DrainJson(json, sizeof(json)) == 0,
        "empty producer returned an event");
}

void TestRingOverflowIsObservable() {
  yunpin::ReplayNativeProducer producer;
  producer.SetEnabled(true);
  const auto event = SyntheticEvent("overflow");
  std::size_t accepted = 0;
  std::size_t rejected = 0;
  for (std::size_t index = 0; index < yunpin::kReplayRingCapacity + 4;
       ++index) {
    if (producer.TryPush(event)) {
      ++accepted;
    } else {
      ++rejected;
    }
  }
  Check(accepted > 0 && rejected > 0,
        "overflow exercise did not cross the ring boundary");
  Check(producer.dropped() > 0, "ring overflow was not counted");
  char json[yunpin::kReplayJsonLimit + 1]{};
  const std::size_t size = producer.DrainJson(json, sizeof(json));
  const std::string encoded(json, size);
  Check(encoded.find("\"type\":\"drop_count\"") != std::string::npos,
        "overflow did not emit drop_count");
}

void TestUtf8TruncationNeverSplitsScalar() {
  yunpin::ReplayText<5> text;
  yunpin::CopyReplayText(&text, "abcd\xE6\x97\xA5");
  Check(text.view() == "abcd", "UTF-8 scalar was split at bound");

  yunpin::ReplayText<16> nul;
  yunpin::CopyReplayText(&nul, std::string_view("safe\0hidden", 11));
  Check(nul.view() == "safe", "NUL was copied into a replay string");

  yunpin::ReplayText<16> overlong;
  yunpin::CopyReplayText(&overlong, std::string_view("\xC0\xAF", 2));
  Check(overlong.view().empty(), "invalid UTF-8 was copied");
}

void TestMalformedEventsAreRejectedBeforeTheRing() {
  yunpin::ReplayNativeProducer producer;
  producer.SetEnabled(true);
  auto event = SyntheticEvent("synthetic");
  event.composition.candidate_count =
      static_cast<std::uint8_t>(yunpin::kReplayCandidateLimit + 1);
  Check(!producer.TryPush(event), "oversized candidate page was accepted");
  event = SyntheticEvent("synthetic");
  event.composition.raw_input.size = 600;
  Check(!producer.TryPush(event), "invalid replay text size was accepted");
  Check(producer.dropped() == 2,
        "rejected native event was not observable as trace loss");
}

void TestCaptureGenerationDoesNotCrossSessionBoundary() {
  yunpin::ReplayNativeProducer producer;
  producer.SetEnabled(true);
  Check(producer.TryPush(SyntheticEvent("old-session")),
        "old session event was not queued");
  producer.SetEnabled(false);
  producer.SetEnabled(true);
  Check(producer.TryPush(SyntheticEvent("new-session")),
        "new session event was not queued");

  char json[yunpin::kReplayJsonLimit + 1]{};
  const std::size_t size = producer.DrainJson(json, sizeof(json));
  const std::string encoded(json, size);
  Check(encoded.find("new-session") != std::string::npos,
        "new session event did not drain");
  Check(encoded.find("old-session") == std::string::npos,
        "queued text crossed the explicit session boundary");
  Check(producer.DrainJson(json, sizeof(json)) == 0,
        "stale session event remained queued");
}

}  // namespace

int main() {
  try {
    TestDisabledByDefaultAndBoundedDrain();
    TestRingOverflowIsObservable();
    TestUtf8TruncationNeverSplitsScalar();
    TestMalformedEventsAreRejectedBeforeTheRing();
    TestCaptureGenerationDoesNotCrossSessionBoundary();
    std::cout << "replay native tests passed\n";
    return 0;
  } catch (const std::exception& error) {
    std::cerr << error.what() << '\n';
    return 1;
  }
}
