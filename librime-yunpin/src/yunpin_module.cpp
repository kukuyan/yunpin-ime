// SPDX-License-Identifier: Apache-2.0
#include <rime/registry.h>
#include <rime_api.h>

#include "rime_yunpin_comment_filter.hpp"
#include "rime_yunpin_corrector.hpp"
#include "rime_yunpin_filter.hpp"

static void rime_yunpin_initialize() {
  using namespace rime;
  LOG(INFO) << "registering components from module 'yunpin'";
  Registry& registry = Registry::instance();
  // A tiny version-locked librime patch lets the YunPin schema select this
  // unique component. Never replace the global `corrector`: other schemas in
  // the same process must retain their upstream behavior.
  registry.Register("yunpin_corrector", new YunPinCorrectorComponent);
  registry.Register("yunpin_comment_filter",
                    new Component<YunPinCommentFilter>);
  registry.Register("yunpin_filter", new Component<YunPinFilter>);
}

static void rime_yunpin_finalize() {}

RIME_REGISTER_MODULE(yunpin)
