// SPDX-License-Identifier: Apache-2.0
#include <rime/registry.h>
#include <rime_api.h>

#include "rime_yunpin_filter.hpp"

static void rime_yunpin_initialize() {
  using namespace rime;
  LOG(INFO) << "registering components from module 'yunpin'";
  Registry::instance().Register("yunpin_filter",
                                new Component<YunPinFilter>);
}

static void rime_yunpin_finalize() {}

RIME_REGISTER_MODULE(yunpin)
