// SPDX-License-Identifier: Apache-2.0
#pragma once

#if defined(__APPLE__)
#include <CommonCrypto/CommonDigest.h>
#include <array>
#include <string>
#include <string_view>

namespace yunpin {
// Internal macOS receipt identity only. Call at snapshot construction, never
// from candidate queries or public diagnostics. CommonCrypto is part of macOS.
inline std::string SnapshotContentDigest(std::string_view contents) {
  if (contents.size() > 64U * 1024U * 1024U)
    return {};
  std::array<unsigned char, CC_SHA256_DIGEST_LENGTH> digest{};
  if (!CC_SHA256(contents.data(), static_cast<CC_LONG>(contents.size()),
                 digest.data()))
    return {};
  constexpr char hex[] = "0123456789abcdef";
  std::string result;
  result.reserve(digest.size() * 2);
  for (const auto byte : digest) {
    result.push_back(hex[byte >> 4]);
    result.push_back(hex[byte & 15]);
  }
  return result;
}
}  // namespace yunpin
#endif
