// SPDX-License-Identifier: Apache-2.0
// API subset verified against librime 33e78140250125871856cdc5b42ddc6a5fcd3cd4.
// Test surface matching the pinned librime Language value semantics.
#pragma once
#include <rime/common.h>
namespace rime {
class Language {
 public:
  explicit Language(const string& name) : name_(name) {}
  string name() const { return name_; }
  bool operator==(const Language& other) const { return name_ == other.name_; }
  static string get_language_component(const string& name) {
    return name.substr(0, name.find('.'));
  }
 private:
  string name_;
};
}  // namespace rime
