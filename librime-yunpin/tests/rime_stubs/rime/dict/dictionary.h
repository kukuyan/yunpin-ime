// SPDX-License-Identifier: Apache-2.0
// API subset verified against librime 33e78140250125871856cdc5b42ddc6a5fcd3cd4.
// Minimal read-only public dictionary surface; no stub userdb is involved.
#pragma once
#include <rime/common.h>
#include <rime/schema.h>
#include <rime/ticket.h>
namespace rime {
using Syllabary = set<string>;
using Code = vector<int>;
struct DictEntry {
  string text, comment, preedit;
  Code code;
  int matching_code_size = 0;
  int commit_count = 0;
};
class Table {
 public:
  explicit Table(Syllabary values) : syllables_(values.begin(), values.end()) {}
  bool GetSyllabary(Syllabary* result) {
    result->insert(syllables_.begin(), syllables_.end()); return true;
  }
  string GetSyllableById(int id) { return syllables_.at(id); }
 private:
  vector<string> syllables_;
};
class Dictionary {
 public:
  // Per-test public fixtures, cleared by their owning test.
  inline static Syllabary test_syllables;
  explicit Dictionary(string name) : name_(std::move(name)),
      table_(New<Table>(test_syllables)) {}
  bool Load() { return !test_syllables.empty(); }
  const an<Table>& primary_table() const { return table_; }
  const string& name() const { return name_; }
  class Component {
   public:
    Dictionary* Create(const Ticket& ticket) {
      string name;
      return ticket.schema && ticket.schema->config()->GetString(
          ticket.name_space + "/dictionary", &name) ? new Dictionary(name)
                                                     : nullptr;
    }
  };
  static Component* Require(const string&) { static Component c; return &c; }
 private:
  string name_;
  an<Table> table_;
};
}  // namespace rime
