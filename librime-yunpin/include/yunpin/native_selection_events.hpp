// SPDX-License-Identifier: Apache-2.0
#pragma once

#include <cstddef>
#include <cstdint>
#include <memory>
#include <string>
#include <string_view>

namespace yunpin {

// The native input hot path publishes only the selected phrase and its
// normalized spelling.  There is deliberately no application, window,
// surrounding-text, or protected-context field: private/password/one-shot
// commits are rejected before an event reaches this queue.
struct NativeSelectionEvent {
  static constexpr std::uint32_t kVersion = 1;

  std::string event_id;
  std::string phrase;
  std::string pinyin;
};

// Process-local, fixed-capacity hand-off for a frontend-owned asynchronous
// adapter.  Producers never wait for a lock and never open a file, database,
// socket, or IPC channel.  Contention/full-queue events are dropped and
// counted so input remains responsive if the adapter is unavailable.
class NativeSelectionEventQueue {
 public:
  static constexpr std::size_t kCapacity = 512;
  static constexpr std::size_t kMaxEventIdBytes = 128;
  static constexpr std::size_t kMaxPhraseBytes = 512;
  static constexpr std::size_t kMaxPinyinBytes = 256;

  static NativeSelectionEventQueue& Instance();
  ~NativeSelectionEventQueue();

  [[nodiscard]] bool TryPublish(std::string_view phrase,
                                std::string_view normalized_pinyin) noexcept;
  [[nodiscard]] bool TryPop(NativeSelectionEvent* event) noexcept;
  [[nodiscard]] std::size_t DiscardAll() noexcept;
  void PausePublishingForSpoolerStop() noexcept;
  void ResumePublishingForSpoolerStart() noexcept;
  [[nodiscard]] std::uint64_t dropped() const noexcept;
  [[nodiscard]] std::size_t size() const noexcept;

 private:
  NativeSelectionEventQueue();
  NativeSelectionEventQueue(const NativeSelectionEventQueue&) = delete;
  NativeSelectionEventQueue& operator=(const NativeSelectionEventQueue&) =
      delete;

  class Impl;
  std::unique_ptr<Impl> impl_;
};

}  // namespace yunpin

// Stable C boundary used by Squirrel and Weasel.  Starting the spooler creates
// one background drain thread in the current frontend process; selection and
// commit callbacks still only enqueue into the fixed-capacity memory queue.
// The path must be an absolute UTF-8 path to the platform's private
// native-events/incoming directory.  Fixed buffers keep ownership and
// allocator ABIs out of the contract.
#if defined(_WIN32)
#define YUNPIN_NATIVE_EVENTS_API __declspec(dllexport)
#elif defined(__GNUC__)
#define YUNPIN_NATIVE_EVENTS_API __attribute__((visibility("default")))
#else
#define YUNPIN_NATIVE_EVENTS_API
#endif

extern "C" {

struct YunPinNativeSelectionEventV1 {
  std::uint32_t version;
  char event_id[129];
  char phrase[513];
  char pinyin[257];
};

YUNPIN_NATIVE_EVENTS_API bool YunPinTryPopNativeSelectionEventV1(
    YunPinNativeSelectionEventV1* event) noexcept;
YUNPIN_NATIVE_EVENTS_API bool YunPinStartNativeSelectionSpoolerV1(
    const char* absolute_utf8_directory) noexcept;
// Windows host entry point.  Resolves FOLDERID_LocalAppData inside the native
// producer instead of trusting a mutable environment variable.  Other
// platforms return false and continue to use the explicit-path API above.
YUNPIN_NATIVE_EVENTS_API bool
YunPinStartDefaultNativeSelectionSpoolerV1() noexcept;
YUNPIN_NATIVE_EVENTS_API void YunPinStopNativeSelectionSpoolerV1() noexcept;
YUNPIN_NATIVE_EVENTS_API std::uint64_t
YunPinDroppedNativeSelectionEventCount() noexcept;
YUNPIN_NATIVE_EVENTS_API std::uint64_t
YunPinNativeSelectionSpoolDropCount() noexcept;

}  // extern "C"
