// SPDX-License-Identifier: Apache-2.0

import Foundation
#if canImport(YunPinMobileCore)
import YunPinMobileCore
#endif

public enum SyncTrigger: String, Codable, Sendable {
    case appActivation
    case backgroundTask
    case manual
    case localCommit
}

public enum MobileSyncError: Error, Equatable, Sendable {
    case endpointMissing
    case credentialMissing
    case operationBusy
    case invalidLocalMutation
    case profileChanged
}

public enum LocalDataPlaneHealthState: Equatable, Sendable {
    case empty
    case snapshot(ValidatedSnapshotFingerprint)
}

public actor MobileSyncCoordinator {
    private enum ActiveOperation: Equatable, Sendable {
        case synchronize
        case refreshStatus
        case commitUpgradeHealth
        case publishSnapshot
        case rollbackSnapshot
        case recordSelection
        case saveExplicit
        case delete
    }

    private let endpoints: EndpointProfileStore
    private let credentials: any OpaqueCredentialStore
    private let binding: any SyncCoreBinding
    private let pathResolver: SyncCorePathResolver
    private let statuses: RedactedSyncStatusStore
    private let now: @Sendable () -> Date
    private let snapshotStateInspector: @Sendable (URL) async throws -> ExactSnapshotState
    private var activeOperation: ActiveOperation?
    private var cancellationRequested = false
    private var bindingOperationActive = false

    public init(
        endpoints: EndpointProfileStore,
        credentials: any OpaqueCredentialStore,
        binding: any SyncCoreBinding,
        pathResolver: SyncCorePathResolver,
        statuses: RedactedSyncStatusStore,
        now: @escaping @Sendable () -> Date = Date.init,
        snapshotStateInspector: @escaping @Sendable (URL) async throws -> ExactSnapshotState = { url in
            try await Task.detached(priority: .utility) {
                try ExactSnapshotInspector.inspectState(at: url)
            }.value
        }
    ) {
        self.endpoints = endpoints
        self.credentials = credentials
        self.binding = binding
        self.pathResolver = pathResolver
        self.statuses = statuses
        self.now = now
        self.snapshotStateInspector = snapshotStateInspector
    }

    @discardableResult
    public func synchronize(
        trigger: SyncTrigger,
        expectedProfileID: UUID? = nil,
        timeoutMilliseconds: Int64 = 30_000
    ) async -> RedactedSyncStatus {
        guard claim(.synchronize) else { return await busyStatus() }
        defer { release(.synchronize) }
        let attempt = now()

        do {
            let (endpoint, credential, paths) = try await prerequisites(expectedProfileID: expectedProfileID)
            let previous = try? await statuses.load()
            try await statuses.save(RedactedSyncStatus(
                phase: .syncing,
                pendingEnvelopeCount: previous?.pendingEnvelopeCount ?? 0,
                cursor: previous?.cursor ?? 0,
                snapshotPresent: previous?.snapshotPresent,
                lastAttemptAt: attempt,
                lastSuccessAt: previous?.lastSuccessAt
            ))
            if let expectedProfileID {
                try await requireSelectedProfile(expectedProfileID)
            }
            let binding = self.binding
            let timeout = boundedTimeout(timeoutMilliseconds)
            let report = try await withActiveBindingOperation {
                try await binding.synchronize(
                    paths: paths,
                    endpoint: endpoint,
                    credentialBlob: credential,
                    timeoutMilliseconds: timeout
                )
            }
            if let expectedProfileID {
                try await requireSelectedProfile(expectedProfileID)
            }
            let result = RedactedSyncStatus(
                phase: .idle,
                pendingEnvelopeCount: report.pending,
                cursor: report.cursor,
                snapshotPresent: true,
                lastAttemptAt: attempt,
                lastSuccessAt: now()
            )
            if let expectedProfileID {
                try await requireSelectedProfile(expectedProfileID)
            }
            try await statuses.save(result)
            if let expectedProfileID {
                try await requireSelectedProfile(expectedProfileID)
            }
            try throwIfCancellationRequested()
            return result
        } catch MobileSyncError.profileChanged {
            return profileLeaseFailureStatus(attempt: attempt)
        } catch {
            return await storeFailure(error, attempt: attempt)
        }
    }

    public func refreshStatus(
        expectedProfileID: UUID,
        timeoutMilliseconds: Int64 = 5_000
    ) async -> RedactedSyncStatus {
        guard claim(.refreshStatus) else { return await busyStatus() }
        defer { release(.refreshStatus) }
        let attempt = now()
        do {
            let (endpoint, credential, paths) = try await prerequisites(expectedProfileID: expectedProfileID)
            let binding = self.binding
            let timeout = boundedTimeout(timeoutMilliseconds)
            let report = try await withActiveBindingOperation {
                try await binding.status(
                    paths: paths,
                    endpoint: endpoint,
                    credentialBlob: credential,
                    timeoutMilliseconds: timeout
                )
            }
            try await requireSelectedProfile(expectedProfileID)
            let previous = try? await statuses.load()
            try await requireSelectedProfile(expectedProfileID)
            let status = RedactedSyncStatus(
                phase: .idle,
                pendingEnvelopeCount: report.pending,
                cursor: report.cursor,
                snapshotPresent: report.snapshotPresent,
                lastAttemptAt: attempt,
                lastSuccessAt: previous?.lastSuccessAt
            )
            try await statuses.save(status)
            try await requireSelectedProfile(expectedProfileID)
            try throwIfCancellationRequested()
            return status
        } catch MobileSyncError.profileChanged {
            return profileLeaseFailureStatus(attempt: attempt)
        } catch {
            return await storeFailure(error, attempt: attempt)
        }
    }

    /// Holds the coordinator's operation-wide claim across local Facade.Status,
    /// exact current-file validation, and the profile-bound journal commit.
    /// Therefore a same-profile sync/publish/rollback/local mutation cannot
    /// promote the snapshot between its fingerprint and health marker.
    public func commitLocalUpgradeHealth(
        expectedProfileID: UUID,
        timeoutMilliseconds: Int64 = 5_000,
        commit: @escaping @Sendable (LocalDataPlaneHealthState) async throws -> Void
    ) async -> Bool {
        guard claim(.commitUpgradeHealth) else { return false }
        defer { release(.commitUpgradeHealth) }
        do {
            let (endpoint, credential, paths) = try await prerequisites(
                expectedProfileID: expectedProfileID
            )
            let binding = self.binding
            let timeout = boundedTimeout(timeoutMilliseconds)
            let report = try await withActiveBindingOperation {
                try await binding.status(
                    paths: paths,
                    endpoint: endpoint,
                    credentialBlob: credential,
                    timeoutMilliseconds: timeout
                )
            }
            try await requireSelectedProfile(expectedProfileID)
            let exactState = try await snapshotStateInspector(paths.privateSnapshotURL)
            try await requireSelectedProfile(expectedProfileID)
            try throwIfCancellationRequested()

            let healthState: LocalDataPlaneHealthState
            switch (report.snapshotPresent, exactState) {
            case (true, .present(let fingerprint)):
                healthState = .snapshot(fingerprint)
            case (false, .absent):
                healthState = .empty
            default:
                return false
            }

            // No coordinator operation can claim the data plane while this
            // async profile-bound commit is suspended on its journal actor.
            try await requireSelectedProfile(expectedProfileID)
            try await commit(healthState)
            try await requireSelectedProfile(expectedProfileID)
            try throwIfCancellationRequested()
            return true
        } catch {
            return false
        }
    }

    public func publishSnapshot(timeoutMilliseconds: Int64 = 10_000) async throws -> SnapshotPublishReport {
        guard claim(.publishSnapshot) else { throw MobileSyncError.operationBusy }
        defer { release(.publishSnapshot) }
        let (endpoint, credential, paths) = try await prerequisites()
        let binding = self.binding
        let timeout = boundedTimeout(timeoutMilliseconds)
        return try await withActiveBindingOperation {
            try await binding.publishSnapshot(
                paths: paths,
                endpoint: endpoint,
                credentialBlob: credential,
                timeoutMilliseconds: timeout
            )
        }
    }

    public func rollbackSnapshot(expectedProfileID: UUID) async throws {
        guard claim(.rollbackSnapshot) else { throw MobileSyncError.operationBusy }
        defer { release(.rollbackSnapshot) }
        let (endpoint, credential, paths) = try await prerequisites(expectedProfileID: expectedProfileID)
        try await requireSelectedProfile(expectedProfileID)
        let binding = self.binding
        try await withActiveBindingOperation {
            try await binding.rollbackSnapshot(paths: paths, endpoint: endpoint, credentialBlob: credential)
        }
        try await requireSelectedProfile(expectedProfileID)
    }

    /// Containing-app-only handoff. Keyboard code has no reference to this
    /// service and phase 1 never invokes learning from an extension callback.
    public func recordSelection(
        text: String,
        pinyin: String,
        privacy: SelectionPrivacyContext,
        timeoutMilliseconds: Int64 = 5_000
    ) async throws -> RecordSelectionReport {
        guard claim(.recordSelection) else { throw MobileSyncError.operationBusy }
        defer { release(.recordSelection) }
        try validateLocalPhrase(text: text, pinyin: pinyin)
        let (endpoint, credential, paths) = try await prerequisites()
        let binding = self.binding
        let timeout = boundedTimeout(timeoutMilliseconds)
        return try await withActiveBindingOperation {
            try await binding.recordSelection(
                paths: paths,
                endpoint: endpoint,
                credentialBlob: credential,
                text: text,
                pinyin: pinyin,
                privacy: privacy,
                timeoutMilliseconds: timeout
            )
        }
    }

    public func saveExplicit(
        text: String,
        pinyin: String,
        useCount: UInt64,
        pinned: Bool,
        timeoutMilliseconds: Int64 = 5_000
    ) async throws {
        guard claim(.saveExplicit) else { throw MobileSyncError.operationBusy }
        defer { release(.saveExplicit) }
        try validateLocalPhrase(text: text, pinyin: pinyin)
        guard useCount > 0, useCount <= UInt64(Int64.max) else {
            throw MobileSyncError.invalidLocalMutation
        }
        let (endpoint, credential, paths) = try await prerequisites()
        let binding = self.binding
        let timeout = boundedTimeout(timeoutMilliseconds)
        try await withActiveBindingOperation {
            try await binding.saveExplicit(
                paths: paths,
                endpoint: endpoint,
                credentialBlob: credential,
                text: text,
                pinyin: pinyin,
                useCount: useCount,
                pinned: pinned,
                timeoutMilliseconds: timeout
            )
        }
    }

    public func delete(
        text: String,
        pinyin: String,
        timeoutMilliseconds: Int64 = 5_000
    ) async throws {
        guard claim(.delete) else { throw MobileSyncError.operationBusy }
        defer { release(.delete) }
        try validateLocalPhrase(text: text, pinyin: pinyin)
        let (endpoint, credential, paths) = try await prerequisites()
        let binding = self.binding
        let timeout = boundedTimeout(timeoutMilliseconds)
        try await withActiveBindingOperation {
            try await binding.delete(
                paths: paths,
                endpoint: endpoint,
                credentialBlob: credential,
                text: text,
                pinyin: pinyin,
                timeoutMilliseconds: timeout
            )
        }
    }

    /// Out-of-band cancellation intentionally does not claim the single-flight
    /// gate. Actor reentrancy lets an expiration callback enter while another
    /// coordinator method awaits the native facade.
    public func cancelCurrentOperation() async {
        guard activeOperation != nil else { return }
        cancellationRequested = true
        guard bindingOperationActive else { return }
        await binding.cancelCurrentOperation()
    }

    /// Actor reentrancy means every operation must reserve the data plane
    /// before its first await. Status-returning calls preserve only the last
    /// counters/timestamps but return a fixed busy gate; mutating calls throw.
    private func claim(_ operation: ActiveOperation) -> Bool {
        guard activeOperation == nil else { return false }
        activeOperation = operation
        cancellationRequested = false
        bindingOperationActive = false
        return true
    }

    private func release(_ operation: ActiveOperation) {
        guard activeOperation == operation else { return }
        activeOperation = nil
        cancellationRequested = false
        bindingOperationActive = false
    }

    private func withActiveBindingOperation<Result: Sendable>(
        _ operation: @Sendable () async throws -> Result
    ) async throws -> Result {
        try throwIfCancellationRequested()
        bindingOperationActive = true
        defer { bindingOperationActive = false }
        let result = try await operation()
        try throwIfCancellationRequested()
        return result
    }

    private func throwIfCancellationRequested() throws {
        guard !cancellationRequested, !Task.isCancelled else {
            throw SyncCoreBindingError.cancelled
        }
    }

    private func prerequisites(
        expectedProfileID: UUID? = nil
    ) async throws -> (SyncEndpointProfile, Data, SyncCorePaths) {
        guard let endpoint = await endpoints.selected() else { throw MobileSyncError.endpointMissing }
        if let expectedProfileID, endpoint.id != expectedProfileID {
            throw MobileSyncError.profileChanged
        }
        guard let credential = try await credentials.load(for: endpoint.id), !credential.isEmpty else {
            throw MobileSyncError.credentialMissing
        }
        if let expectedProfileID {
            try await requireSelectedProfile(expectedProfileID)
        }
        return (endpoint, credential, try pathResolver.paths(for: endpoint.id))
    }

    private func requireSelectedProfile(_ expectedProfileID: UUID) async throws {
        guard await endpoints.selected()?.id == expectedProfileID else {
            throw MobileSyncError.profileChanged
        }
    }

    private func boundedTimeout(_ requested: Int64) -> Int64 {
        min(max(requested, 1_000), 300_000)
    }

    private func validateLocalPhrase(text: String, pinyin: String) throws {
        guard !text.isEmpty,
              !pinyin.isEmpty,
              text.utf8.count <= 512,
              pinyin.utf8.count <= 256 else {
            throw MobileSyncError.invalidLocalMutation
        }
    }

    private func storeFailure(_ error: Error, attempt: Date) async -> RedactedSyncStatus {
        let previous = try? await statuses.load()
        let status = RedactedSyncStatus(
            phase: Self.phase(for: error),
            code: Self.redactedCode(for: error),
            pendingEnvelopeCount: previous?.pendingEnvelopeCount ?? 0,
            cursor: previous?.cursor ?? 0,
            snapshotPresent: previous?.snapshotPresent,
            lastAttemptAt: attempt,
            lastSuccessAt: previous?.lastSuccessAt
        )
        try? await statuses.save(status)
        return status
    }

    private func busyStatus() async -> RedactedSyncStatus {
        let stored = try? await statuses.load()
        return RedactedSyncStatus(
            phase: .blocked,
            code: .operationBusy,
            pendingEnvelopeCount: stored?.pendingEnvelopeCount ?? 0,
            cursor: stored?.cursor ?? 0,
            snapshotPresent: stored?.snapshotPresent,
            lastAttemptAt: stored?.lastAttemptAt,
            lastSuccessAt: stored?.lastSuccessAt
        )
    }

    private static func phase(for error: Error) -> SyncStatusPhase {
        switch error {
        case MobileSyncError.endpointMissing, MobileSyncError.credentialMissing:
            return .waiting
        case MobileSyncError.operationBusy:
            return .blocked
        case MobileSyncError.profileChanged:
            return .blocked
        case MobileSyncError.invalidLocalMutation:
            return .failed
        case SyncCoreBindingError.unavailable,
             SyncCoreBindingError.authorizationRequired,
             SyncCoreBindingError.remoteConflict:
            return .blocked
        default:
            return .failed
        }
    }

    private static func redactedCode(for error: Error) -> RedactedSyncCode {
        switch error {
        case MobileSyncError.endpointMissing: return .endpointMissing
        case MobileSyncError.credentialMissing: return .credentialMissing
        case MobileSyncError.operationBusy: return .operationBusy
        case MobileSyncError.profileChanged: return .protocolViolation
        case MobileSyncError.invalidLocalMutation: return .protocolViolation
        case SyncCoreBindingError.unavailable: return .syncCoreUnavailable
        case SyncCoreBindingError.authorizationRequired: return .authenticationRejected
        case SyncCoreBindingError.remoteConflict: return .sequenceConflict
        case SyncCoreBindingError.remoteRejected: return .endpointRejected
        case SyncCoreBindingError.remoteUnavailable,
             SyncCoreBindingError.networkUnavailable,
             SyncCoreBindingError.deadlineExceeded: return .connectivity
        case SyncCoreBindingError.cancelled, is CancellationError: return .cancelled
        case SyncCoreBindingError.invalidConfiguration: return .protocolViolation
        case SyncCoreBindingError.localState, is CredentialStoreError: return .storage
        default: return .storage
        }
    }

    private func profileLeaseFailureStatus(attempt: Date) -> RedactedSyncStatus {
        RedactedSyncStatus(
            phase: .blocked,
            code: .protocolViolation,
            pendingEnvelopeCount: 0,
            cursor: 0,
            lastAttemptAt: attempt
        )
    }
}
