// SPDX-License-Identifier: Apache-2.0

import Foundation

enum YunPinAppConfiguration {
    static let appGroupIdentifier: String = requiredInfoValue("YunPinAppGroup")
    static let backgroundTaskIdentifier: String = requiredInfoValue("YunPinBackgroundTaskIdentifier")

    private static func requiredInfoValue(_ key: String) -> String {
        guard let value = Bundle.main.object(forInfoDictionaryKey: key) as? String,
              !value.isEmpty else {
            preconditionFailure("Missing required non-secret app configuration")
        }
        return value
    }
}
