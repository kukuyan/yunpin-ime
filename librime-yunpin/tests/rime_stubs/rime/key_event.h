// SPDX-License-Identifier: Apache-2.0
// Test-only subset matching librime 1.13.0 through 1.17.0.
// Mirrors locked librime commit 33e78140250125871856cdc5b42ddc6a5fcd3cd4.
#pragma once

#include <rime/common.h>

inline constexpr int XK_BackSpace = 0xff08;
inline constexpr int kShiftMask = 1 << 0;
inline constexpr int kControlMask = 1 << 2;
inline constexpr int kReleaseMask = 1 << 30;

namespace rime {
class KeyEvent {
 public:
  KeyEvent() = default;
  KeyEvent(int keycode, int modifier)
      : keycode_(keycode), modifier_(modifier) {}
  int keycode() const { return keycode_; }
  int modifier() const { return modifier_; }
 private:
  int keycode_{0};
  int modifier_{0};
};
}  // namespace rime
