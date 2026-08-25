// SPDX-License-Identifier: Apache-2.0

import Foundation

enum KeyboardConfiguration {
    static let appGroupIdentifier: String = {
        guard let value = Bundle.main.object(forInfoDictionaryKey: "YunPinAppGroup") as? String,
              !value.isEmpty else {
            preconditionFailure("Missing required non-secret App Group configuration")
        }
        return value
    }()
}
