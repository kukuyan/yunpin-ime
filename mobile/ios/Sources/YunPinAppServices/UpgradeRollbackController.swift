// SPDX-License-Identifier: Apache-2.0

import Foundation
#if canImport(YunPinMobileCore)
import YunPinMobileCore
#endif

public enum UpgradeRecoveryGate: String, Codable, Equatable, Sendable {
    case matchingValidatedRollbackRequired
}

public struct UpgradeRecoveryPlan: Equatable, Sendable {
    public let shouldRestoreSnapshot: Bool
    public let gate: UpgradeRecoveryGate?

    public init(shouldRestoreSnapshot: Bool, gate: UpgradeRecoveryGate? = nil) {
        self.shouldRestoreSnapshot = shouldRestoreSnapshot
        self.gate = gate
    }
}

public enum UpgradeRollbackError: Error, Equatable, Sendable {
    case snapshotNotValidated
    case snapshotGenerationExhausted
    case profileLeaseChanged
}

public struct UpgradeSnapshotRevision: Codable, Equatable, Sendable {
    public let generation: UInt64
    public let sha256Hex: String
}

/// Executes data-plane work only after upgrade preparation returns normally.
/// The caller owns redacted UI state, while this boundary guarantees that a
/// corrupt/unreadable journal cannot fall through to sync or publication.
public enum FailClosedUpgradePreparation {
    @MainActor
    public static func runIfPrepared<Result: Sendable>(
        preparation: () async throws -> Void,
        dataPlane: () async -> Result
    ) async -> Result? {
        do {
            try await preparation()
        } catch {
            return nil
        }
        return await dataPlane()
    }
}

/// Shares one preparation task among concurrent lifecycle callbacks. A
/// profile becomes reusable only after the operation returns successfully;
/// callers can never interpret an in-progress profile as prepared.
public actor UpgradePreparationSingleFlight {
    private struct InFlight: Sendable {
        let profileID: UUID
        let sequence: UInt64
        let task: Task<Void, Error>
    }

    private var completedProfileID: UUID?
    private var sequence: UInt64 = 0
    private var inFlight: InFlight?
    private var activeCallers = 0

    public init() {}

    public func prepare(
        profileID: UUID,
        operation: @escaping @Sendable () async throws -> Void
    ) async throws {
        activeCallers += 1
        defer { activeCallers -= 1 }
        if completedProfileID == profileID {
            try Task.checkCancellation()
            return
        }
        let active: InFlight
        if let inFlight {
            active = inFlight
        } else {
            sequence &+= 1
            if sequence == 0 { sequence = 1 }
            active = InFlight(
                profileID: profileID,
                sequence: sequence,
                task: Task { try await operation() }
            )
            inFlight = active
        }
        do {
            try await active.task.value
            if inFlight?.sequence == active.sequence {
                completedProfileID = active.profileID
                inFlight = nil
            } else if completedProfileID != active.profileID {
                throw UpgradeRollbackError.profileLeaseChanged
            }
            try Task.checkCancellation()
        } catch {
            if inFlight?.sequence == active.sequence {
                completedProfileID = nil
                inFlight = nil
            }
            throw error
        }
        guard active.profileID == profileID else {
            return try await prepare(profileID: profileID, operation: operation)
        }
    }

    func activeCallerCount() -> Int { activeCallers }
}

/// Commits an upgrade health marker only from one explicitly named endpoint
/// profile. The coordinator retains its operation-wide claim from local
/// status through exact snapshot validation and this journal actor's commit.
public enum ProfileBoundUpgradeHealthVerifier {
    public static func markHealthy(
        expectedProfileID: UUID,
        build: String,
        coordinator: MobileSyncCoordinator,
        controller: UpgradeRollbackController
    ) async -> Bool {
        await coordinator.commitLocalUpgradeHealth(
            expectedProfileID: expectedProfileID
        ) { healthState in
            switch healthState {
            case .snapshot(let fingerprint):
                try await controller.markLocalDataPlaneHealthy(
                    expectedProfileID: expectedProfileID,
                    build: build,
                    currentSnapshot: fingerprint
                )
            case .empty:
                try await controller.markLocalEmptyDataPlaneHealthy(
                    expectedProfileID: expectedProfileID,
                    build: build
                )
            }
        }
    }
}

/// Detects an app build that repeatedly launches without reaching the healthy
/// marker. It can restore the last-known-good snapshot; binary rollback remains
/// an explicit App Store/TestFlight/MDM gate outside this repository.
public actor UpgradeRollbackController {
    private struct Journal: Codable, Sendable {
        let schema: Int
        var lastKnownGoodBuild: String?
        var lastKnownGoodSnapshot: UpgradeSnapshotRevision?
        var pendingBuild: String?
        var pendingLaunchCount: Int
    }

    private let store: AtomicVersionedFileStore<Journal>
    private let profileID: UUID
    private var journal: Journal

    public init(directory: URL, profileID: UUID) throws {
        let store = try AtomicVersionedFileStore<Journal>(directory: directory, name: "upgrade-journal")
        self.store = store
        self.profileID = profileID
        let loaded = try store.load()
        self.journal = loaded?.schema == 2
            ? loaded!
            : Journal(schema: 2, lastKnownGoodBuild: nil, lastKnownGoodSnapshot: nil, pendingBuild: nil, pendingLaunchCount: 0)
    }

    public func beginLaunch(
        expectedProfileID: UUID,
        build: String,
        validatedRollback: ValidatedSnapshotFingerprint?
    ) throws -> UpgradeRecoveryPlan {
        try requireProfile(expectedProfileID)
        if journal.lastKnownGoodBuild == build {
            return UpgradeRecoveryPlan(shouldRestoreSnapshot: false)
        }
        if journal.pendingBuild == build {
            journal.pendingLaunchCount += 1
        } else {
            journal.pendingBuild = build
            journal.pendingLaunchCount = 1
        }
        try store.save(journal)
        guard journal.pendingLaunchCount >= 2 else {
            return UpgradeRecoveryPlan(shouldRestoreSnapshot: false)
        }
        // A newly paired profile has no recoverable snapshot generation yet.
        // It must remain able to run its first sync after repeated launches;
        // no rollback file or digest is fabricated for that empty state.
        guard let expected = journal.lastKnownGoodSnapshot else {
            return UpgradeRecoveryPlan(shouldRestoreSnapshot: false)
        }
        guard validatedRollback?.sha256Hex == expected.sha256Hex else {
            return UpgradeRecoveryPlan(
                shouldRestoreSnapshot: false,
                gate: .matchingValidatedRollbackRequired
            )
        }
        return UpgradeRecoveryPlan(shouldRestoreSnapshot: true)
    }

    /// A build cannot become last-known-good merely because its UI launched.
    /// The caller must first complete local shared-core status and validate the
    /// exact current snapshot (not an in-memory rollback fallback).
    @discardableResult
    public func markLocalDataPlaneHealthy(
        expectedProfileID: UUID,
        build: String,
        currentSnapshot: ValidatedSnapshotFingerprint
    ) throws -> UpgradeSnapshotRevision {
        try requireProfile(expectedProfileID)
        let generation: UInt64
        if let previous = journal.lastKnownGoodSnapshot,
           previous.sha256Hex == currentSnapshot.sha256Hex {
            generation = previous.generation
        } else if let previous = journal.lastKnownGoodSnapshot {
            guard previous.generation < UInt64.max else {
                throw UpgradeRollbackError.snapshotGenerationExhausted
            }
            generation = previous.generation + 1
        } else {
            generation = 1
        }
        let revision = UpgradeSnapshotRevision(
            generation: generation,
            sha256Hex: currentSnapshot.sha256Hex
        )
        journal.lastKnownGoodBuild = build
        journal.lastKnownGoodSnapshot = revision
        journal.pendingBuild = nil
        journal.pendingLaunchCount = 0
        try store.save(journal)
        return revision
    }

    /// Records a legitimate pre-first-sync state only when this profile has
    /// never established a snapshot LKG. Once a validated generation exists,
    /// absence cannot erase its digest or relax the rollback gate.
    public func markLocalEmptyDataPlaneHealthy(
        expectedProfileID: UUID,
        build: String
    ) throws {
        try requireProfile(expectedProfileID)
        guard journal.lastKnownGoodSnapshot == nil else {
            throw UpgradeRollbackError.snapshotNotValidated
        }
        journal.lastKnownGoodBuild = build
        journal.pendingBuild = nil
        journal.pendingLaunchCount = 0
        try store.save(journal)
    }

    private func requireProfile(_ expectedProfileID: UUID) throws {
        guard expectedProfileID == profileID else {
            throw UpgradeRollbackError.profileLeaseChanged
        }
    }
}
