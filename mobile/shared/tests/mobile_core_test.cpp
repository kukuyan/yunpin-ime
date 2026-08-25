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
  assert(yunpin_mobile_abi_version() == YUNPIN_MOBILE_ABI_VERSION);
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
             engine, query.data(), query.size(), 8,
             YUNPIN_MOBILE_CONTEXT_NONE, Collect, &candidates,
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
             engine, query.data(), query.size(), 8,
             YUNPIN_MOBILE_CONTEXT_NONE, Collect, &candidates,
             &returned) == YUNPIN_MOBILE_OK);
  assert(returned == 1);
  assert(candidates.front() == "首次记住");

  const std::string partially_invalid =
      "phrase\tpinyin\tsource\tuse_count\tpinned\n"
      "新的有效行\txin de you xiao hang\tsynced_learning\t2\tfalse\n"
      "损坏行\tnot_a_valid_pinyin\tsynced_learning\t2\tfalse\n";
  assert(yunpin_mobile_engine_load_snapshot(
             engine,
             reinterpret_cast<const std::uint8_t*>(partially_invalid.data()),
             partially_invalid.size(), &accepted,
             &rejected) == YUNPIN_MOBILE_INVALID_SNAPSHOT);
  assert(accepted == 1);
  assert(rejected == 1);
  candidates.clear();
  assert(yunpin_mobile_engine_query(
             engine, query.data(), query.size(), 8,
             YUNPIN_MOBILE_CONTEXT_NONE, Collect, &candidates,
             &returned) == YUNPIN_MOBILE_OK);
  assert(returned == 1);
  assert(candidates.front() == "首次记住");

  const std::string unsafe_text =
      "phrase\tpinyin\tsource\tuse_count\tpinned\n"
      "方向\xE2\x80\xAE控制\tfang xiang kong zhi\tsynced_learning\t2\tfalse\n";
  assert(yunpin_mobile_engine_load_snapshot(
             engine, reinterpret_cast<const std::uint8_t*>(unsafe_text.data()),
             unsafe_text.size(), &accepted,
             &rejected) == YUNPIN_MOBILE_INVALID_SNAPSHOT);
  assert(accepted == 0);
  assert(rejected == 1);

  const std::string c1_control_text =
      "phrase\tpinyin\tsource\tuse_count\tpinned\n"
      "合成\xC2\x85控制\the cheng kong zhi\tsynced_learning\t2\tfalse\n";
  assert(yunpin_mobile_engine_load_snapshot(
             engine,
             reinterpret_cast<const std::uint8_t*>(c1_control_text.data()),
             c1_control_text.size(), &accepted,
             &rejected) == YUNPIN_MOBILE_INVALID_SNAPSHOT);
  assert(accepted == 0);
  assert(rejected == 1);

  candidates.clear();
  assert(yunpin_mobile_engine_query(
             engine, query.data(), query.size(), 8,
             YUNPIN_MOBILE_CONTEXT_PASSWORD |
                 YUNPIN_MOBILE_CONTEXT_NO_PERSONALIZED_LEARNING,
             Collect, &candidates, &returned) == YUNPIN_MOBILE_OK);
  assert(returned == 0);
  assert(candidates.empty());

  assert(yunpin_mobile_engine_query(
             engine, query.data(), query.size(), 8, 1U << 31, Collect,
             &candidates, &returned) == YUNPIN_MOBILE_INVALID_ARGUMENT);

  yunpin_mobile_engine_destroy(engine);
  return 0;
}
