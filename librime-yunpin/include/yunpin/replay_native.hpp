// SPDX-License-Identifier: Apache-2.0
#pragma once

#include <array>
#include <atomic>
#include <cstddef>
#include <cstdint>
#include <string_view>

namespace yunpin {

constexpr std::size_t kReplayCandidateLimit = 8;
constexpr std::size_t kReplayRingCapacity = 64;
constexpr std::size_t kReplayJsonLimit = 8 * 1024;

enum class ReplayEventType : std::uint8_t {
  kComposition,
  kSelect,
  kCommit,
  kBackspace,
  kDelete,
  kAbort,
};

template <std::size_t Capacity>
struct ReplayText {
  std::array<char, Capacity + 1> bytes{};
  std::uint16_t size{0};

  [[nodiscard]] std::string_view view() const noexcept {
    return std::string_view(bytes.data(), size);
  }
};

using ReplayInputText = ReplayText<512>;
using ReplayCandidateText = ReplayText<256>;
using ReplayCommitText = ReplayText<2048>;

struct ReplayCandidate {
  ReplayCandidateText text;
  bool is_correction{false};
  bool highlighted{false};
};

struct ReplayComposition {
  ReplayInputText raw_input;
  ReplayInputText normalized_pinyin;
  std::uint16_t caret_byte{0};
  std::uint8_t candidate_count{0};
  bool exact_path_available{false};
  std::array<ReplayCandidate, kReplayCandidateLimit> candidates{};
};

struct ReplayNativeEvent {
  ReplayEventType type{ReplayEventType::kComposition};
  std::uint64_t monotonic_us{0};
  std::uint64_t utc_unix_us{0};
  ReplayComposition composition;
  std::uint8_t selection_rank{0};
  ReplayCandidateText selection_text;
  std::uint32_t edit_count{0};
  ReplayCommitText final_text;
};

template <std::size_t Capacity>
void CopyReplayText(ReplayText<Capacity>* destination,
                    std::string_view source) noexcept {
  if (!destination) {
    return;
  }
  std::size_t input = 0;
  std::size_t output = 0;
  while (input < source.size() && output < Capacity) {
    const auto first = static_cast<unsigned char>(source[input]);
    if (first == 0) {
      break;
    }
    std::size_t width = 0;
    if (first < 0x80) {
      width = 1;
    } else if ((first & 0xe0) == 0xc0) {
      width = 2;
    } else if ((first & 0xf0) == 0xe0) {
      width = 3;
    } else if ((first & 0xf8) == 0xf0) {
      width = 4;
    } else {
      break;
    }
    if (input + width > source.size() || output + width > Capacity) {
      break;
    }
    // Reject overlong forms, UTF-16 surrogate code points, and values above
    // U+10FFFF. The background JSON consumer is strict UTF-8 and NUL-free.
    if ((width == 2 && first < 0xc2) ||
        (width == 3 && first == 0xe0 &&
         static_cast<unsigned char>(source[input + 1]) < 0xa0) ||
        (width == 3 && first == 0xed &&
         static_cast<unsigned char>(source[input + 1]) >= 0xa0) ||
        (width == 4 && first == 0xf0 &&
         static_cast<unsigned char>(source[input + 1]) < 0x90) ||
        (width == 4 && first == 0xf4 &&
         static_cast<unsigned char>(source[input + 1]) >= 0x90) ||
        (width == 4 && first > 0xf4)) {
      break;
    }
    bool valid = true;
    for (std::size_t index = 1; index < width; ++index) {
      const auto continuation =
          static_cast<unsigned char>(source[input + index]);
      valid = valid && ((continuation & 0xc0) == 0x80);
    }
    if (!valid) {
      break;
    }
    for (std::size_t index = 0; index < width; ++index) {
      destination->bytes[output++] = source[input++];
    }
  }
  destination->size = static_cast<std::uint16_t>(output);
  destination->bytes[output] = '\0';
}

class ReplayNativeProducer {
 public:
  ReplayNativeProducer() = default;
  ReplayNativeProducer(const ReplayNativeProducer&) = delete;
  ReplayNativeProducer& operator=(const ReplayNativeProducer&) = delete;

  void SetEnabled(bool enabled) noexcept;
  [[nodiscard]] bool enabled() const noexcept;
  [[nodiscard]] bool TryPush(const ReplayNativeEvent& event) noexcept;

  // Called only by the background platform sink. Returns one JSON object
  // without a trailing newline, or zero when the queue is empty.
  std::size_t DrainJson(char* output, std::size_t capacity);

  void RememberComposition(const ReplayComposition& composition) noexcept;
  [[nodiscard]] ReplayComposition LastComposition() const noexcept;

  [[nodiscard]] std::uint64_t dropped() const noexcept;

 private:
  bool TryPop(ReplayNativeEvent* event) noexcept;

  std::array<ReplayNativeEvent, kReplayRingCapacity> ring_{};
  std::atomic<std::size_t> head_{0};
  std::atomic<std::size_t> tail_{0};
  std::atomic<std::uint64_t> dropped_{0};
  std::atomic<bool> enabled_{false};
  ReplayComposition last_composition_{};
};

ReplayNativeProducer& GlobalReplayNativeProducer() noexcept;
std::uint64_t ReplayMonotonicMicros() noexcept;
std::uint64_t ReplayUtcUnixMicros() noexcept;

}  // namespace yunpin
