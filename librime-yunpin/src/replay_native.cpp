// SPDX-License-Identifier: Apache-2.0
#include "yunpin/replay_native.hpp"

#include <chrono>
#include <cstring>
#include <sstream>
#include <string>

namespace yunpin {
namespace {

const char* EventTypeName(ReplayEventType type) noexcept {
  switch (type) {
    case ReplayEventType::kComposition:
      return "composition_snapshot";
    case ReplayEventType::kSelect:
      return "select";
    case ReplayEventType::kCommit:
      return "commit";
    case ReplayEventType::kBackspace:
      return "backspace";
    case ReplayEventType::kDelete:
      return "delete";
    case ReplayEventType::kAbort:
      return "abort";
  }
  return "abort";
}

void AppendJsonString(std::ostringstream& output, std::string_view value) {
  static constexpr char kHex[] = "0123456789abcdef";
  output << '"';
  for (const unsigned char byte : value) {
    switch (byte) {
      case '"':
        output << "\\\"";
        break;
      case '\\':
        output << "\\\\";
        break;
      case '\b':
        output << "\\b";
        break;
      case '\f':
        output << "\\f";
        break;
      case '\n':
        output << "\\n";
        break;
      case '\r':
        output << "\\r";
        break;
      case '\t':
        output << "\\t";
        break;
      default:
        if (byte < 0x20) {
          output << "\\u00" << kHex[(byte >> 4) & 0x0f]
                 << kHex[byte & 0x0f];
        } else {
          output << static_cast<char>(byte);
        }
    }
  }
  output << '"';
}

void AppendComposition(std::ostringstream& output,
                       const ReplayComposition& composition) {
  output << "\"composition\":{";
  output << "\"raw_input\":";
  AppendJsonString(output, composition.raw_input.view());
  output << ",\"normalized_pinyin\":";
  AppendJsonString(output, composition.normalized_pinyin.view());
  output << ",\"caret_byte\":" << composition.caret_byte;
  output << ",\"exact_path_available\":"
         << (composition.exact_path_available ? "true" : "false");
  output << ",\"candidates\":[";
  for (std::size_t index = 0; index < composition.candidate_count; ++index) {
    if (index != 0) {
      output << ',';
    }
    const auto& candidate = composition.candidates[index];
    output << "{\"text\":";
    AppendJsonString(output, candidate.text.view());
    output << ",\"is_correction\":"
           << (candidate.is_correction ? "true" : "false")
           << ",\"highlighted\":"
           << (candidate.highlighted ? "true" : "false") << '}';
  }
  output << "]}";
}

std::string SerializeEvent(const ReplayNativeEvent& event) {
  std::ostringstream output;
  output << "{\"version\":\"yunpin.replay.native.v1\"";
  output << ",\"monotonic_us\":" << event.monotonic_us;
  output << ",\"utc_unix_us\":" << event.utc_unix_us;
  output << ",\"type\":\"" << EventTypeName(event.type) << "\",";
  AppendComposition(output, event.composition);
  if (event.type == ReplayEventType::kSelect) {
    output << ",\"selection\":{\"rank\":"
           << static_cast<unsigned int>(event.selection_rank)
           << ",\"text\":";
    AppendJsonString(output, event.selection_text.view());
    output << '}';
  }
  if (event.type == ReplayEventType::kBackspace ||
      event.type == ReplayEventType::kDelete) {
    output << ",\"edit_count\":" << event.edit_count;
  }
  if (event.type == ReplayEventType::kCommit) {
    output << ",\"final_text\":";
    AppendJsonString(output, event.final_text.view());
  }
  output << '}';
  return output.str();
}

std::string SerializeDrop(std::uint64_t count) {
  std::ostringstream output;
  output << "{\"version\":\"yunpin.replay.native.v1\","
            "\"monotonic_us\":"
         << ReplayMonotonicMicros() << ",\"utc_unix_us\":"
         << ReplayUtcUnixMicros()
         << ",\"type\":\"drop_count\",\"drop_count\":" << count
         << '}';
  return output.str();
}

bool IsValidUtf8(std::string_view value) noexcept {
  std::size_t index = 0;
  while (index < value.size()) {
    const auto first = static_cast<unsigned char>(value[index]);
    if (first == 0) {
      return false;
    }
    std::size_t width = 0;
    if (first < 0x80) {
      width = 1;
    } else if (first >= 0xc2 && first <= 0xdf) {
      width = 2;
    } else if (first >= 0xe0 && first <= 0xef) {
      width = 3;
    } else if (first >= 0xf0 && first <= 0xf4) {
      width = 4;
    } else {
      return false;
    }
    if (index + width > value.size()) {
      return false;
    }
    for (std::size_t offset = 1; offset < width; ++offset) {
      const auto continuation =
          static_cast<unsigned char>(value[index + offset]);
      if ((continuation & 0xc0) != 0x80) {
        return false;
      }
    }
    if ((width == 3 && first == 0xe0 &&
         static_cast<unsigned char>(value[index + 1]) < 0xa0) ||
        (width == 3 && first == 0xed &&
         static_cast<unsigned char>(value[index + 1]) >= 0xa0) ||
        (width == 4 && first == 0xf0 &&
         static_cast<unsigned char>(value[index + 1]) < 0x90) ||
        (width == 4 && first == 0xf4 &&
         static_cast<unsigned char>(value[index + 1]) >= 0x90)) {
      return false;
    }
    index += width;
  }
  return true;
}

template <std::size_t Capacity>
bool IsValidText(const ReplayText<Capacity>& text,
                 bool allow_empty) noexcept {
  if (text.size > Capacity || (!allow_empty && text.size == 0)) {
    return false;
  }
  return IsValidUtf8(text.view());
}

bool IsValidComposition(const ReplayComposition& composition) noexcept {
  if (composition.candidate_count > kReplayCandidateLimit ||
      composition.caret_byte > composition.raw_input.size ||
      !IsValidText(composition.raw_input, true) ||
      !IsValidText(composition.normalized_pinyin, true) ||
      (composition.caret_byte != 0 &&
       composition.caret_byte != composition.raw_input.size &&
       (static_cast<unsigned char>(
            composition.raw_input.bytes[composition.caret_byte]) &
        0xc0) == 0x80)) {
    return false;
  }
  std::size_t highlighted = 0;
  for (std::size_t index = 0; index < composition.candidate_count; ++index) {
    const auto& candidate = composition.candidates[index];
    if (!IsValidText(candidate.text, false)) {
      return false;
    }
    highlighted += candidate.highlighted ? 1 : 0;
  }
  return highlighted <= 1;
}

bool IsValidEvent(const ReplayNativeEvent& event) noexcept {
  if (event.utc_unix_us == 0 || !IsValidComposition(event.composition)) {
    return false;
  }
  switch (event.type) {
    case ReplayEventType::kSelect: {
      const auto rank = static_cast<std::size_t>(event.selection_rank);
      return rank != 0 && rank <= event.composition.candidate_count &&
             IsValidText(event.selection_text, false) &&
             event.selection_text.view() ==
                 event.composition.candidates[rank - 1].text.view();
    }
    case ReplayEventType::kCommit:
      return IsValidText(event.final_text, false);
    case ReplayEventType::kBackspace:
    case ReplayEventType::kDelete:
      return event.edit_count != 0 && event.edit_count <= 1024;
    case ReplayEventType::kComposition:
    case ReplayEventType::kAbort:
      return true;
  }
  return false;
}

}  // namespace

void ReplayNativeProducer::SetEnabled(bool enabled) noexcept {
  enabled_.store(enabled, std::memory_order_release);
}

bool ReplayNativeProducer::enabled() const noexcept {
  return enabled_.load(std::memory_order_acquire);
}

bool ReplayNativeProducer::TryPush(const ReplayNativeEvent& event) noexcept {
  if (!enabled()) {
    return false;
  }
  if (!IsValidEvent(event)) {
    dropped_.fetch_add(1, std::memory_order_relaxed);
    return false;
  }
  const std::size_t head = head_.load(std::memory_order_relaxed);
  const std::size_t next = (head + 1) % kReplayRingCapacity;
  if (next == tail_.load(std::memory_order_acquire)) {
    dropped_.fetch_add(1, std::memory_order_relaxed);
    return false;
  }
  ring_[head] = event;
  head_.store(next, std::memory_order_release);
  return true;
}

bool ReplayNativeProducer::TryPop(ReplayNativeEvent* event) noexcept {
  const std::size_t tail = tail_.load(std::memory_order_relaxed);
  if (tail == head_.load(std::memory_order_acquire)) {
    return false;
  }
  *event = ring_[tail];
  tail_.store((tail + 1) % kReplayRingCapacity, std::memory_order_release);
  return true;
}

std::size_t ReplayNativeProducer::DrainJson(char* output,
                                            std::size_t capacity) {
  if (!output || capacity == 0) {
    return 0;
  }
  const std::uint64_t dropped = dropped_.exchange(0);
  if (dropped != 0) {
    const std::string encoded = SerializeDrop(dropped);
    if (encoded.size() >= capacity || encoded.size() > kReplayJsonLimit) {
      dropped_.fetch_add(dropped, std::memory_order_relaxed);
      return 0;
    }
    std::memcpy(output, encoded.data(), encoded.size());
    output[encoded.size()] = '\0';
    return encoded.size();
  }

  ReplayNativeEvent event;
  while (TryPop(&event)) {
    const std::string encoded = SerializeEvent(event);
    if (encoded.size() >= capacity || encoded.size() > kReplayJsonLimit) {
      dropped_.fetch_add(1, std::memory_order_relaxed);
      continue;
    }
    std::memcpy(output, encoded.data(), encoded.size());
    output[encoded.size()] = '\0';
    return encoded.size();
  }
  output[0] = '\0';
  return 0;
}

void ReplayNativeProducer::RememberComposition(
    const ReplayComposition& composition) noexcept {
  last_composition_ = composition;
}

ReplayComposition ReplayNativeProducer::LastComposition() const noexcept {
  return last_composition_;
}

std::uint64_t ReplayNativeProducer::dropped() const noexcept {
  return dropped_.load(std::memory_order_relaxed);
}

ReplayNativeProducer& GlobalReplayNativeProducer() noexcept {
  static ReplayNativeProducer producer;
  return producer;
}

std::uint64_t ReplayMonotonicMicros() noexcept {
  const auto now = std::chrono::steady_clock::now().time_since_epoch();
  return static_cast<std::uint64_t>(
      std::chrono::duration_cast<std::chrono::microseconds>(now).count());
}

std::uint64_t ReplayUtcUnixMicros() noexcept {
  const auto now = std::chrono::system_clock::now().time_since_epoch();
  return static_cast<std::uint64_t>(
      std::chrono::duration_cast<std::chrono::microseconds>(now).count());
}

}  // namespace yunpin
