// SPDX-License-Identifier: Apache-2.0
// Test-only subset matching librime 1.13.0 through 1.17.0.
// Mirrors locked librime commit 33e78140250125871856cdc5b42ddc6a5fcd3cd4.
#pragma once

#include <rime/segmentation.h>

namespace rime {
class Composition : public vector<Segment> {};
}  // namespace rime
