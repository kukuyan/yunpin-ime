// SPDX-License-Identifier: Apache-2.0

import Foundation
#if canImport(YunPinMobileCore)
import YunPinMobileCore
#endif

public struct KeyboardPrivacyContext: Equatable, Sendable {
    public let hasFullAccess: Bool
    public let isSecureTextContext: Bool
    public let isIncognitoContext: Bool

    public init(hasFullAccess: Bool, isSecureTextContext: Bool, isIncognitoContext: Bool) {
        self.hasFullAccess = hasFullAccess
        self.isSecureTextContext = isSecureTextContext
        self.isIncognitoContext = isIncognitoContext
    }
}

public enum KeyboardPrivacyPolicy {
    public static func permitsPrivateCandidates(in context: KeyboardPrivacyContext) -> Bool {
        context.hasFullAccess && !context.isSecureTextContext && !context.isIncognitoContext
    }

    public static func permitsLearning(in context: KeyboardPrivacyContext) -> Bool {
        // Phase 1 is strictly read-only. Full Access may unlock snapshot reads
        // in a separately reviewed build, but never learning handoff/writes.
        false
    }
}

/// The extension obtains immutable bytes only after the privacy gate. It has no
/// API for credentials, endpoint configuration, synchronization, or mutation.
public struct KeyboardPrivateSnapshotAccess: Sendable {
    private let resolver: ReadOnlySnapshotResolver

    public init(appGroupSnapshotRoot: URL, selectedProfileID: UUID) {
        self.resolver = ReadOnlySnapshotResolver(
            snapshotURL: appGroupSnapshotRoot
                .appendingPathComponent("Profiles", isDirectory: true)
                .appendingPathComponent(selectedProfileID.uuidString.lowercased(), isDirectory: true)
                .appendingPathComponent("private.tsv")
        )
    }

    public func snapshotData(in context: KeyboardPrivacyContext) -> Data? {
        guard KeyboardPrivacyPolicy.permitsPrivateCandidates(in: context) else { return nil }
        return try? resolver.resolve().data
    }
}
