// SPDX-License-Identifier: Apache-2.0
#pragma once

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct YunPinMobileEngine YunPinMobileEngine;

typedef enum YunPinMobileStatus {
  YUNPIN_MOBILE_OK = 0,
  YUNPIN_MOBILE_INVALID_ARGUMENT = 1,
  YUNPIN_MOBILE_INVALID_SNAPSHOT = 2,
  YUNPIN_MOBILE_RESOURCE_ERROR = 3,
} YunPinMobileStatus;

typedef struct YunPinMobileCandidateView {
  const char* text;
  size_t text_size;
  const char* full_pinyin;
  size_t full_pinyin_size;
  uint64_t use_count;
  int32_t origin;
  int32_t match;
  int32_t pinned;
  int64_t last_used_day;
} YunPinMobileCandidateView;

typedef void (*YunPinMobileCandidateCallback)(
    void* context, const YunPinMobileCandidateView* candidate);

// Creates an empty, network-free candidate engine. The returned object is
// shared by an Android InputMethodService or iOS keyboard extension, while the
// containing app owns account login and background synchronization.
YunPinMobileEngine* yunpin_mobile_engine_create(void);
void yunpin_mobile_engine_destroy(YunPinMobileEngine* engine);

// Atomically replaces the immutable personal snapshot after full validation.
// Invalid input leaves the previously loaded generation active. No phrase is
// logged or returned in an error string.
YunPinMobileStatus yunpin_mobile_engine_load_snapshot(
    YunPinMobileEngine* engine, const uint8_t* bytes, size_t size,
    size_t* accepted_rows, size_t* rejected_rows);

// Invokes callback synchronously for at most eight bounded candidates. View
// pointers remain valid only for the duration of the callback.
YunPinMobileStatus yunpin_mobile_engine_query(
    YunPinMobileEngine* engine, const char* input, size_t input_size,
    size_t limit, YunPinMobileCandidateCallback callback, void* context,
    size_t* returned_candidates);

#ifdef __cplusplus
}
#endif
