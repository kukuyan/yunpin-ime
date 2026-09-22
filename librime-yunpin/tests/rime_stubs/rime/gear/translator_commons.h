// SPDX-License-Identifier: Apache-2.0
// API subset verified against librime 33e78140250125871856cdc5b42ddc6a5fcd3cd4.
// The Phrase identity used by Rime's ScriptTranslator learning callback.
#pragma once
#include <rime/candidate.h>
#include <rime/dict/dictionary.h>
#include <rime/language.h>
namespace rime {
class Phrase : public Candidate {
 public:
  Phrase(const Language* language, const string& type, size_t start, size_t end,
         const an<DictEntry>& entry)
      : Candidate(type, start, end), language_(language), entry_(entry) {}
  const string& text() const override { return entry_->text; }
  string comment() const override { return entry_->comment; }
  string preedit() const override { return entry_->preedit; }
  const Language* language() const { return language_; }
  Code& code() const { return entry_->code; }
  const DictEntry& entry() const { return *entry_; }
  bool is_exact_match() const {
    return entry_->matching_code_size == 0 ||
           entry_->matching_code_size == static_cast<int>(entry_->code.size());
  }
 private:
  const Language* language_;
  an<DictEntry> entry_;
};
}  // namespace rime
