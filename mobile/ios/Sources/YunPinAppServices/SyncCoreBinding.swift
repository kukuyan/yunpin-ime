// SPDX-License-Identifier: Apache-2.0

import Foundation
#if canImport(YunPinMobileCore)
import YunPinMobileCore
#endif

public struct SyncCorePaths: Equatable, Sendable {
    public let encryptedDatabaseURL: URL
    public let privateSnapshotURL: URL

    public init(encryptedDatabaseURL: URL, privateSnapshotURL: URL) throws {
        guard encryptedDatabaseURL.isFileURL,
              privateSnapshotURL.isFileURL,
              encryptedDatabaseURL.path.hasPrefix("/"),
              privateSnapshotURL.path.hasPrefix("/") else {
            throw SyncCoreBindingError.invalidConfiguration
        }
        self.encryptedDatabaseURL = encryptedDatabaseURL
        self.privateSnapshotURL = privateSnapshotURL
    }
}

public struct SyncCoreReport: Codable, Equatable, Sendable {
    public let rounds: Int
    public let uploaded: Int
    public let downloaded: Int
    public let cursor: Int64
    public let pending: UInt64
    public let snapshotRows: Int
    public let snapshotChanged: Bool

    public init(rounds: Int, uploaded: Int, downloaded: Int, cursor: Int64, pending: UInt64, snapshotRows: Int, snapshotChanged: Bool) {
        self.rounds = rounds
        self.uploaded = uploaded
        self.downloaded = downloaded
        self.cursor = cursor
        self.pending = pending
        self.snapshotRows = snapshotRows
        self.snapshotChanged = snapshotChanged
    }
}

public struct SyncCoreStatus: Codable, Equatable, Sendable {
    public let cursor: Int64
    public let pending: UInt64
    public let prepared: Bool
    public let snapshotPresent: Bool
    public let rollbackPresent: Bool
    public let controlPlaneGate: String

    public init(cursor: Int64, pending: UInt64, prepared: Bool, snapshotPresent: Bool, rollbackPresent: Bool, controlPlaneGate: String) {
        self.cursor = cursor
        self.pending = pending
        self.prepared = prepared
        self.snapshotPresent = snapshotPresent
        self.rollbackPresent = rollbackPresent
        self.controlPlaneGate = controlPlaneGate
    }
}

public struct SnapshotPublishReport: Codable, Equatable, Sendable {
    public let generation: UInt64
    public let rows: Int
    public let changed: Bool
    public let rollbackAvailable: Bool

    public init(generation: UInt64, rows: Int, changed: Bool, rollbackAvailable: Bool) {
        self.generation = generation
        self.rows = rows
        self.changed = changed
        self.rollbackAvailable = rollbackAvailable
    }
}

public struct SelectionPrivacyContext: Equatable, Sendable {
    public let passwordField: Bool
    public let privateMode: Bool
    public let oneTimeInput: Bool
    public let noPersonalizedLearning: Bool

    public init(
        passwordField: Bool,
        privateMode: Bool,
        oneTimeInput: Bool,
        noPersonalizedLearning: Bool
    ) {
        self.passwordField = passwordField
        self.privateMode = privateMode
        self.oneTimeInput = oneTimeInput
        self.noPersonalizedLearning = noPersonalizedLearning
    }
}

public struct RecordSelectionReport: Codable, Equatable, Sendable {
    public let recorded: Bool
    public let useCount: UInt64
    public let syncEligible: Bool

    public init(recorded: Bool, useCount: UInt64, syncEligible: Bool) {
        self.recorded = recorded
        self.useCount = useCount
        self.syncEligible = syncEligible
    }
}

enum GeneratedBindingResultValidator {
    static func validateSync(
        rounds: Int,
        uploaded: Int,
        downloaded: Int,
        cursor: Int64,
        snapshotRows: Int
    ) throws {
        guard rounds >= 0,
              uploaded >= 0,
              downloaded >= 0,
              cursor >= 0,
              snapshotRows >= 0 else {
            throw SyncCoreBindingError.localState
        }
    }

    static func validateStatus(cursor: Int64, controlPlaneGate: String) throws {
        guard cursor >= 0,
              controlPlaneGate.isEmpty
                || controlPlaneGate == "signed_roster_chain_required" else {
            throw SyncCoreBindingError.localState
        }
    }

    static func validateSnapshot(rows: Int) throws {
        guard rows >= 0 else { throw SyncCoreBindingError.localState }
    }
}

public enum SyncCoreBindingError: Error, Equatable, Sendable {
    case unavailable
    case invalidConfiguration
    case authorizationRequired
    case remoteConflict
    case remoteRejected
    case remoteUnavailable
    case networkUnavailable
    case deadlineExceeded
    case cancelled
    case localState
}

/// Keeps already-decoded fixed errors intact. Only an opaque native NSError
/// crosses the fallback string mapper, and that mapper recognizes fixed codes
/// without persisting or exposing the raw description.
func normalizeGeneratedBindingError(_ error: Error) -> SyncCoreBindingError {
    if let bindingError = error as? SyncCoreBindingError {
        return bindingError
    }
    let description = (error as NSError).localizedDescription.lowercased()
    for code in generatedBindingKnownErrorCodes where description.contains(code) {
        return syncCoreBindingError(forRedactedCode: code)
    }
    return .localState
}

func syncCoreBindingError(forRedactedCode code: String?) -> SyncCoreBindingError {
    switch code {
    case "authorization_required": return .authorizationRequired
    case "remote_conflict": return .remoteConflict
    case "remote_rejected": return .remoteRejected
    case "remote_unavailable": return .remoteUnavailable
    case "network_unavailable": return .networkUnavailable
    case "deadline_exceeded": return .deadlineExceeded
    case "cancelled": return .cancelled
    default: return .localState
    }
}

private let generatedBindingKnownErrorCodes = [
    "authorization_required",
    "remote_conflict",
    "remote_rejected",
    "remote_unavailable",
    "network_unavailable",
    "deadline_exceeded",
    "cancelled",
    "local_state_error",
]

/// Thread-safe operation intent used by generated bindings whose native
/// facade does not exist until after potentially expensive local setup. A
/// cancellation requested after `beginOperation` is sticky for that lease: if
/// the facade has not registered yet, registration immediately invokes its
/// native cancellation callback; if it is already running, cancellation is
/// invoked out of band on the active facade.
final class ActiveFacadeCancellationRegistry: @unchecked Sendable {
    struct Lease: Equatable, Sendable {
        fileprivate let sequence: UInt64
    }

    private enum Phase: Equatable {
        case awaitingFacade
        case registered
    }

    private final class CancellationAction: @unchecked Sendable {
        private let action: @Sendable () -> Void

        init(_ action: @escaping @Sendable () -> Void) {
            self.action = action
        }

        func invoke() { action() }
    }

    private struct Entry {
        let lease: Lease
        var phase: Phase
        var cancellationRequested: Bool
        var cancellationAction: CancellationAction?
    }

    private let lock = NSLock()
    private var sequence: UInt64 = 0
    private var active: Entry?

    func beginOperation() -> Lease? {
        lock.lock()
        defer { lock.unlock() }
        guard active == nil else { return nil }
        sequence &+= 1
        if sequence == 0 { sequence = 1 }
        let lease = Lease(sequence: sequence)
        active = Entry(
            lease: lease,
            phase: .awaitingFacade,
            cancellationRequested: false,
            cancellationAction: nil
        )
        return lease
    }

    @discardableResult
    func registerFacade(
        for lease: Lease,
        cancellationAction: @escaping @Sendable () -> Void
    ) -> Bool {
        let action = CancellationAction(cancellationAction)
        let shouldCancel: Bool
        lock.lock()
        guard var entry = active,
              entry.lease == lease,
              entry.phase == .awaitingFacade else {
            lock.unlock()
            return false
        }
        entry.phase = .registered
        entry.cancellationAction = action
        shouldCancel = entry.cancellationRequested
        active = entry
        lock.unlock()
        if shouldCancel { action.invoke() }
        return true
    }

    func cancelCurrentOperation() {
        let action: CancellationAction?
        lock.lock()
        if var entry = active {
            entry.cancellationRequested = true
            action = entry.cancellationAction
            active = entry
        } else {
            action = nil
        }
        lock.unlock()
        action?.invoke()
    }

    func finishOperation(_ lease: Lease) {
        lock.lock()
        if active?.lease == lease {
            active = nil
        }
        lock.unlock()
    }
}

/// Thin native boundary for the generated `mobile/synccore.Facade` binding.
/// Implementations must open a short-lived facade, call one operation, and
/// close it so mutable key buffers are best-effort overwritten and the session
/// is released. No plaintext envelope, CRDT, queue, cursor, or phrase-store API
/// is represented in Swift.
public protocol SyncCoreBinding: Sendable {
    func synchronize(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreReport
    func status(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreStatus
    func publishSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SnapshotPublishReport
    func rollbackSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data) async throws
    func recordSelection(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, text: String, pinyin: String, privacy: SelectionPrivacyContext, timeoutMilliseconds: Int64) async throws -> RecordSelectionReport
    func saveExplicit(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, text: String, pinyin: String, useCount: UInt64, pinned: Bool, timeoutMilliseconds: Int64) async throws
    func delete(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, text: String, pinyin: String, timeoutMilliseconds: Int64) async throws
    func cancelCurrentOperation() async
}

/// Older test doubles and configuration-only shells fail closed for newly
/// added app-owned local mutation entrypoints.
public extension SyncCoreBinding {
    func recordSelection(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, text: String, pinyin: String, privacy: SelectionPrivacyContext, timeoutMilliseconds: Int64) async throws -> RecordSelectionReport { throw SyncCoreBindingError.unavailable }
    func saveExplicit(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, text: String, pinyin: String, useCount: UInt64, pinned: Bool, timeoutMilliseconds: Int64) async throws { throw SyncCoreBindingError.unavailable }
    func delete(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, text: String, pinyin: String, timeoutMilliseconds: Int64) async throws { throw SyncCoreBindingError.unavailable }
    func cancelCurrentOperation() async {}
}

/// Safe default until the generated gomobile framework is linked. It does not
/// fall back to a second protocol implementation.
public struct UnavailableSyncCoreBinding: SyncCoreBinding {
    public init() {}
    public func synchronize(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreReport { throw SyncCoreBindingError.unavailable }
    public func status(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreStatus { throw SyncCoreBindingError.unavailable }
    public func publishSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SnapshotPublishReport { throw SyncCoreBindingError.unavailable }
    public func rollbackSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data) async throws { throw SyncCoreBindingError.unavailable }
}
