// SPDX-License-Identifier: Apache-2.0
#pragma once

#include <rime/dict/corrector.h>

#include "yunpin/typo_correction.hpp"

namespace rime {

class YunPinCorrector : public Corrector {
 public:
  explicit YunPinCorrector(const Ticket& ticket);
  ~YunPinCorrector() override = default;

  void ToleranceSearch(const Prism& prism,
                       const string& key,
                       corrector::Corrections* results,
                       size_t tolerance) override;

 private:
  yunpin::TypoCorrectionOptions options_;
};

// Registered under the unique `yunpin_corrector` name. A version-locked,
// three-line librime compatibility patch lets ScriptTranslator select it from
// schema configuration without changing the upstream default component.
class YunPinCorrectorComponent : public Corrector::Component {
 public:
  Corrector* Create(const Ticket& ticket) noexcept override;
};

}  // namespace rime
