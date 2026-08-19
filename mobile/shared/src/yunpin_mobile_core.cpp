// SPDX-License-Identifier: Apache-2.0
#include "yunpin_mobile_core.h"

#include <algorithm>
#include <cstddef>
#include <cstdint>
#include <memory>
#include <sstream>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

#include "yunpin/snapshot_store.hpp"

namespace {

constexpr std::size_t kMaxSnapshotBytes = 64U << 20;
constexpr std::size_t kMaxInputBytes = 256;
constexpr std::size_t kMaxCandidates = 8;

}  // namespace

struct YunPinMobileEngine {
  yunpin::SnapshotStore store;
};

extern "C" YunPinMobileEngine* yunpin_mobile_engine_create(void) {
  try {
    return new YunPinMobileEngine();
  } catch (...) {
    return nullptr;
  }
}

extern "C" void yunpin_mobile_engine_destroy(YunPinMobileEngine* engine) {
  delete engine;
}

extern "C" YunPinMobileStatus yunpin_mobile_engine_load_snapshot(
    YunPinMobileEngine* engine, const std::uint8_t* bytes, std::size_t size,
    std::size_t* accepted_rows, std::size_t* rejected_rows) {
  if (accepted_rows) {
    *accepted_rows = 0;
  }
  if (rejected_rows) {
    *rejected_rows = 0;
  }
  if (!engine || !bytes || size == 0 || size > kMaxSnapshotBytes) {
    return YUNPIN_MOBILE_INVALID_ARGUMENT;
  }
  try {
    const std::string snapshot(reinterpret_cast<const char*>(bytes), size);
    std::istringstream input(snapshot);
    auto parsed = yunpin::ParsePrivateSnapshot(input);
    if (accepted_rows) {
      *accepted_rows = parsed.accepted_rows;
    }
    if (rejected_rows) {
      *rejected_rows = parsed.rejected_rows;
    }
    if (!parsed.header_valid) {
      return YUNPIN_MOBILE_INVALID_SNAPSHOT;
    }
    engine->store.Replace(std::move(parsed.entries));
    return YUNPIN_MOBILE_OK;
  } catch (...) {
    return YUNPIN_MOBILE_RESOURCE_ERROR;
  }
}

extern "C" YunPinMobileStatus yunpin_mobile_engine_query(
    YunPinMobileEngine* engine, const char* input, std::size_t input_size,
    std::size_t limit, YunPinMobileCandidateCallback callback, void* context,
    std::size_t* returned_candidates) {
  if (returned_candidates) {
    *returned_candidates = 0;
  }
  if (!engine || !input || input_size == 0 || input_size > kMaxInputBytes ||
      !callback || !returned_candidates || limit == 0) {
    return YUNPIN_MOBILE_INVALID_ARGUMENT;
  }
  try {
    const std::string_view query(input, input_size);
    const auto candidates =
        engine->store.Query(query, std::min(limit, kMaxCandidates));
    for (const auto& candidate : candidates) {
      const YunPinMobileCandidateView view{
          candidate.text.data(),
          candidate.text.size(),
          candidate.full_pinyin.data(),
          candidate.full_pinyin.size(),
          candidate.use_count,
          static_cast<std::int32_t>(candidate.origin),
          static_cast<std::int32_t>(candidate.match),
          candidate.pinned ? 1 : 0,
          candidate.last_used_day,
      };
      callback(context, &view);
    }
    *returned_candidates = candidates.size();
    return YUNPIN_MOBILE_OK;
  } catch (...) {
    return YUNPIN_MOBILE_RESOURCE_ERROR;
  }
}
