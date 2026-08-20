// SPDX-License-Identifier: Apache-2.0

import Foundation

public enum SyncStatusPhase: String, Codable, Equatable, Sendable {
    case idle
    case syncing
    case waiting
    case blocked
    case failed
}

public enum RedactedSyncCode: String, Codable, Equatable, Sendable {
    case endpointMissing
    case credentialMissing
    case syncCoreUnavailable
    case endpointRejected
    case authenticationRejected
    case connectivity
    case protocolViolation
    case sequenceConflict
    case operationBusy
    case storage
    case cancelled
}

/// Deliberately has no free-form message, URL, token, phrase, account ID, or
/// device ID field. Only fixed diagnostic codes can cross into UI/logging.
public struct RedactedSyncStatus: Codable, Equatable, Sendable {
    public let phase: SyncStatusPhase
    public let code: RedactedSyncCode?
    public let pendingEnvelopeCount: UInt64
    public let cursor: Int64
    public let snapshotPresent: Bool?
    public let lastAttemptAt: Date?
    public let lastSuccessAt: Date?

    public init(
        phase: SyncStatusPhase,
        code: RedactedSyncCode? = nil,
        pendingEnvelopeCount: UInt64,
        cursor: Int64,
        snapshotPresent: Bool? = nil,
        lastAttemptAt: Date? = nil,
        lastSuccessAt: Date? = nil
    ) {
        self.phase = phase
        self.code = code
        self.pendingEnvelopeCount = pendingEnvelopeCount
        self.cursor = cursor
        self.snapshotPresent = snapshotPresent
        self.lastAttemptAt = lastAttemptAt
        self.lastSuccessAt = lastSuccessAt
    }
}

public actor RedactedSyncStatusStore {
    private let store: AtomicVersionedFileStore<RedactedSyncStatus>

    public init(directory: URL) throws {
        self.store = try AtomicVersionedFileStore(directory: directory, name: "sync-status")
    }

    public func load() throws -> RedactedSyncStatus? { try store.load() }
    public func save(_ status: RedactedSyncStatus) throws { try store.save(status) }
}
