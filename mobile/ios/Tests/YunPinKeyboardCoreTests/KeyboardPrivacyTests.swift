// SPDX-License-Identifier: Apache-2.0

import Testing
@testable import YunPinKeyboardCore

@Test func privateCandidatesRequireFullAccessAndNonSensitiveContext() {
    #expect(!KeyboardPrivacyPolicy.permitsPrivateCandidates(in: .init(
        hasFullAccess: false,
        isSecureTextContext: false,
        isIncognitoContext: false
    )))
    #expect(!KeyboardPrivacyPolicy.permitsPrivateCandidates(in: .init(
        hasFullAccess: true,
        isSecureTextContext: true,
        isIncognitoContext: false
    )))
    #expect(!KeyboardPrivacyPolicy.permitsLearning(in: .init(
        hasFullAccess: true,
        isSecureTextContext: false,
        isIncognitoContext: true
    )))
    #expect(KeyboardPrivacyPolicy.permitsPrivateCandidates(in: .init(
        hasFullAccess: true,
        isSecureTextContext: false,
        isIncognitoContext: false
    )))
    #expect(!KeyboardPrivacyPolicy.permitsLearning(in: .init(
        hasFullAccess: true,
        isSecureTextContext: false,
        isIncognitoContext: false
    )))
}
