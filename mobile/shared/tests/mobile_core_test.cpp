// SPDX-License-Identifier: Apache-2.0
#include "yunpin_mobile_core.h"

#include <cassert>
#include <cstdint>
#include <string>
#include <vector>

namespace {

void Collect(void* context, const YunPinMobileCandidateView* candidate) {
  assert(context != nullptr);
  assert(candidate != nullptr);
  auto* values = static_cast<std::vector<std::string>*>(context);
  values->emplace_back(candidate->text, candidate->text_size);
}

}  // namespace

int main() {
  YunPinMobileEngine* engine = yunpin_mobile_engine_create();
  assert(engine != nullptr);

  const std::string snapshot =
      "phrase\tpinyin\tsource\tuse_count\tpinned\n"
      "首次记住\tshou ci ji zhu\tsynced_learning\t1\tfalse\n"
      "高频习惯\tgao pin xi guan\tsynced_learning\t8\ttrue\n";
  std::size_t accepted = 0;
  std::size_t rejected = 0;
  assert(yunpin_mobile_engine_load_snapshot(
             engine, reinterpret_cast<const std::uint8_t*>(snapshot.data()),
             snapshot.size(), &accepted, &rejected) == YUNPIN_MOBILE_OK);
  assert(accepted == 2);
  assert(rejected == 0);

  std::vector<std::string> candidates;
  std::size_t returned = 0;
  const std::string query = "shoucijizhu";
  assert(yunpin_mobile_engine_query(
             engine, query.data(), query.size(), 8, Collect, &candidates,
             &returned) == YUNPIN_MOBILE_OK);
  assert(returned == 1);
  assert(candidates.size() == 1);
  assert(candidates.front() == "首次记住");

  const std::string invalid = "not-a-snapshot\n";
  assert(yunpin_mobile_engine_load_snapshot(
             engine, reinterpret_cast<const std::uint8_t*>(invalid.data()),
             invalid.size(), &accepted,
             &rejected) == YUNPIN_MOBILE_INVALID_SNAPSHOT);
  candidates.clear();
  assert(yunpin_mobile_engine_query(
             engine, query.data(), query.size(), 8, Collect, &candidates,
             &returned) == YUNPIN_MOBILE_OK);
  assert(returned == 1);
  assert(candidates.front() == "首次记住");

  yunpin_mobile_engine_destroy(engine);
  return 0;
}
