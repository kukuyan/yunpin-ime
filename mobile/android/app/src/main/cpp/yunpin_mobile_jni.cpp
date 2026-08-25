// SPDX-License-Identifier: Apache-2.0
#include <jni.h>

#include <algorithm>
#include <cstdint>
#include <string>
#include <vector>

#include "yunpin_mobile_core.h"

namespace {

YunPinMobileEngine* FromHandle(jlong handle) {
  return reinterpret_cast<YunPinMobileEngine*>(static_cast<intptr_t>(handle));
}

void CollectCandidate(void* context,
                      const YunPinMobileCandidateView* candidate) {
  if (context == nullptr || candidate == nullptr || candidate->text == nullptr) {
    return;
  }
  auto* values = static_cast<std::vector<std::string>*>(context);
  values->emplace_back(candidate->text, candidate->text_size);
}

jobjectArray EmptyByteArray(JNIEnv* env) {
  jclass byte_array_class = env->FindClass("[B");
  if (byte_array_class == nullptr) {
    return nullptr;
  }
  return env->NewObjectArray(0, byte_array_class, nullptr);
}

}  // namespace

extern "C" JNIEXPORT jint JNICALL
Java_io_github_kukuyan_yunpin_android_ime_NativeCandidateEngine_nativeAbiVersion(
    JNIEnv*, jobject) {
  return static_cast<jint>(yunpin_mobile_abi_version());
}

extern "C" JNIEXPORT jlong JNICALL
Java_io_github_kukuyan_yunpin_android_ime_NativeCandidateEngine_nativeCreate(
    JNIEnv*, jobject) {
  return static_cast<jlong>(
      reinterpret_cast<intptr_t>(yunpin_mobile_engine_create()));
}

extern "C" JNIEXPORT void JNICALL
Java_io_github_kukuyan_yunpin_android_ime_NativeCandidateEngine_nativeDestroy(
    JNIEnv*, jobject, jlong handle) {
  yunpin_mobile_engine_destroy(FromHandle(handle));
}

extern "C" JNIEXPORT jint JNICALL
Java_io_github_kukuyan_yunpin_android_ime_NativeCandidateEngine_nativeLoadSnapshot(
    JNIEnv* env, jobject, jlong handle, jbyteArray bytes) {
  if (handle == 0 || bytes == nullptr) {
    return static_cast<jint>(YUNPIN_MOBILE_INVALID_ARGUMENT);
  }
  const jsize size = env->GetArrayLength(bytes);
  jbyte* data = env->GetByteArrayElements(bytes, nullptr);
  if (data == nullptr) {
    return static_cast<jint>(YUNPIN_MOBILE_RESOURCE_ERROR);
  }
  size_t accepted = 0;
  size_t rejected = 0;
  const YunPinMobileStatus status = yunpin_mobile_engine_load_snapshot(
      FromHandle(handle), reinterpret_cast<const uint8_t*>(data),
      static_cast<size_t>(size), &accepted, &rejected);
  env->ReleaseByteArrayElements(bytes, data, JNI_ABORT);
  return static_cast<jint>(status);
}

extern "C" JNIEXPORT jobjectArray JNICALL
Java_io_github_kukuyan_yunpin_android_ime_NativeCandidateEngine_nativeQueryUtf8(
    JNIEnv* env, jobject, jlong handle, jbyteArray input, jint limit,
    jint context_flags) {
  if (handle == 0 || input == nullptr || limit <= 0) {
    return EmptyByteArray(env);
  }
  const jsize size = env->GetArrayLength(input);
  jbyte* data = env->GetByteArrayElements(input, nullptr);
  if (data == nullptr) {
    return EmptyByteArray(env);
  }
  std::vector<std::string> candidates;
  size_t returned = 0;
  const YunPinMobileStatus status = yunpin_mobile_engine_query(
      FromHandle(handle), reinterpret_cast<const char*>(data),
      static_cast<size_t>(size), static_cast<size_t>(std::min(limit, 8)),
      static_cast<uint32_t>(context_flags), CollectCandidate, &candidates,
      &returned);
  env->ReleaseByteArrayElements(input, data, JNI_ABORT);
  if (status != YUNPIN_MOBILE_OK || returned != candidates.size()) {
    return EmptyByteArray(env);
  }

  jclass byte_array_class = env->FindClass("[B");
  if (byte_array_class == nullptr) {
    return nullptr;
  }
  jobjectArray output = env->NewObjectArray(
      static_cast<jsize>(candidates.size()), byte_array_class, nullptr);
  if (output == nullptr) {
    return nullptr;
  }
  for (size_t index = 0; index < candidates.size(); ++index) {
    const std::string& candidate = candidates[index];
    jbyteArray encoded = env->NewByteArray(static_cast<jsize>(candidate.size()));
    if (encoded == nullptr) {
      return nullptr;
    }
    env->SetByteArrayRegion(encoded, 0, static_cast<jsize>(candidate.size()),
                            reinterpret_cast<const jbyte*>(candidate.data()));
    env->SetObjectArrayElement(output, static_cast<jsize>(index), encoded);
    env->DeleteLocalRef(encoded);
  }
  return output;
}
