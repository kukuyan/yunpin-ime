// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing
import YunPinMobileCore
@testable import YunPinAppServices

@Test func coordinatorDelegatesTheEntireDataPlaneToSharedSyncCore() async throws {
    let root = temporaryDirectory()
    let endpointStore = try EndpointProfileStore(directory: root.appendingPathComponent("state"))
    let profile = try SyncEndpointProfile(
        displayName: "Synthetic relay",
        endpoint: "https://sync.example.test",
        allowsPrivateHTTP: false
    )
    try await endpointStore.saveAndSelect(profile)
    let credentials = SyntheticCredentialStore(blobs: [profile.id: Data("opaque-fixture".utf8)])
    let statusStore = try RedactedSyncStatusStore(directory: root.appendingPathComponent("state"))
    let binding = SyntheticSyncCoreBinding()
    let coordinator = MobileSyncCoordinator(
        endpoints: endpointStore,
        credentials: credentials,
        binding: binding,
        pathResolver: try syntheticPathResolver(root: root),
        statuses: statusStore
    )

    let result = await coordinator.synchronize(trigger: .manual)
    #expect(result.phase == .idle)
    #expect(result.cursor == 7)
    #expect(result.pendingEnvelopeCount == 2)
    #expect(await binding.synchronizeCalls == 1)
}

@Test func appOwnedLocalMutationsReachTheSharedOutboxBinding() async throws {
    let binding = LocalMutationCapturingBinding()
    let coordinator = try await configuredCoordinator(binding: binding).coordinator

    let normal = try await coordinator.recordSelection(
        text: "合成离线候选",
        pinyin: "he cheng li xian hou xuan",
        privacy: SelectionPrivacyContext(
            passwordField: false,
            privateMode: false,
            oneTimeInput: false,
            noPersonalizedLearning: false
        )
    )
    let protected = try await coordinator.recordSelection(
        text: "合成保护上下文",
        pinyin: "he cheng bao hu shang xia wen",
        privacy: SelectionPrivacyContext(
            passwordField: false,
            privateMode: false,
            oneTimeInput: false,
            noPersonalizedLearning: true
        )
    )
    try await coordinator.saveExplicit(
        text: "合成显式保存",
        pinyin: "he cheng xian shi bao cun",
        useCount: 4,
        pinned: true
    )
    try await coordinator.delete(
        text: "合成删除",
        pinyin: "he cheng shan chu"
    )
    let publish = try await coordinator.publishSnapshot()

    #expect(normal == RecordSelectionReport(recorded: true, useCount: 1, syncEligible: true))
    #expect(protected == RecordSelectionReport(recorded: false, useCount: 0, syncEligible: false))
    #expect(publish.changed)
    let capture = await binding.capture()
    #expect(capture.recordSelectionContexts.count == 2)
    #expect(capture.recordSelectionContexts[0].noPersonalizedLearning == false)
    #expect(capture.recordSelectionContexts[1].noPersonalizedLearning == true)
    #expect(capture.saveExplicitCount == 1)
    #expect(capture.savedUseCount == 4)
    #expect(capture.savedPinned == true)
    #expect(capture.deleteCount == 1)
    #expect(capture.publishCount == 1)
}

@Test func missingGeneratedBindingIsAnExplicitGate() async throws {
    let root = temporaryDirectory()
    let endpointStore = try EndpointProfileStore(directory: root.appendingPathComponent("state"))
    let profile = try SyncEndpointProfile(
        displayName: "Synthetic relay",
        endpoint: "https://sync.example.test",
        allowsPrivateHTTP: false
    )
    try await endpointStore.saveAndSelect(profile)
    let coordinator = MobileSyncCoordinator(
        endpoints: endpointStore,
        credentials: SyntheticCredentialStore(blobs: [profile.id: Data("opaque-fixture".utf8)]),
        binding: UnavailableSyncCoreBinding(),
        pathResolver: try syntheticPathResolver(root: root),
        statuses: try RedactedSyncStatusStore(directory: root.appendingPathComponent("state"))
    )
    let result = await coordinator.synchronize(trigger: .backgroundTask)
    #expect(result.phase == .blocked)
    #expect(result.code == .syncCoreUnavailable)
}

@Test func endpointCredentialNeverFollowsAProfileSwitch() async throws {
    let root = temporaryDirectory()
    let endpointStore = try EndpointProfileStore(directory: root.appendingPathComponent("state"))
    let first = try SyncEndpointProfile(
        displayName: "Synthetic relay one",
        endpoint: "https://one.example.test",
        allowsPrivateHTTP: false
    )
    let second = try SyncEndpointProfile(
        displayName: "Synthetic relay two",
        endpoint: "https://two.example.test",
        allowsPrivateHTTP: false
    )
    try await endpointStore.saveAndSelect(first)
    try await endpointStore.saveAndSelect(second)
    let binding = SyntheticSyncCoreBinding()
    let coordinator = MobileSyncCoordinator(
        endpoints: endpointStore,
        credentials: SyntheticCredentialStore(blobs: [first.id: Data("opaque-fixture".utf8)]),
        binding: binding,
        pathResolver: try syntheticPathResolver(root: root),
        statuses: try RedactedSyncStatusStore(directory: root.appendingPathComponent("state"))
    )

    let result = await coordinator.synchronize(trigger: .manual)
    #expect(result.phase == .waiting)
    #expect(result.code == .credentialMissing)
    #expect(await binding.synchronizeCalls == 0)
}

@Test func endpointBindingIsImmutableForAnExistingProfileID() async throws {
    let store = try EndpointProfileStore(directory: temporaryDirectory())
    let profile = try SyncEndpointProfile(
        displayName: "Synthetic original",
        endpoint: "https://one.example.test",
        allowsPrivateHTTP: false
    )
    try await store.saveAndSelect(profile)
    let changedEndpoint = try SyncEndpointProfile(
        id: profile.id,
        displayName: "Synthetic changed",
        endpoint: "https://two.example.test",
        allowsPrivateHTTP: false
    )
    await #expect(throws: EndpointProfileStoreError.endpointBindingImmutable) {
        try await store.saveAndSelect(changedEndpoint)
    }
}

@Test func corruptCurrentGenerationCannotReplaceLastGoodPrevious() throws {
    struct Fixture: Codable, Equatable, Sendable { let value: Int }

    let root = temporaryDirectory()
    let store = try AtomicVersionedFileStore<Fixture>(directory: root, name: "synthetic")
    try store.save(Fixture(value: 1))
    try store.save(Fixture(value: 2))
    try Data("{synthetic-corruption".utf8).write(
        to: root.appendingPathComponent("synthetic.json"),
        options: .atomic
    )
    try store.save(Fixture(value: 3))
    try Data("{synthetic-corruption".utf8).write(
        to: root.appendingPathComponent("synthetic.json"),
        options: .atomic
    )

    #expect(try store.load() == Fixture(value: 1))
}

@Test
@MainActor
func doublyCorruptUpgradeJournalCannotReachTheSyncBinding() async throws {
    let journalDirectory = temporaryDirectory()
    try FileManager.default.createDirectory(
        at: journalDirectory,
        withIntermediateDirectories: true
    )
    let corruption = Data("{synthetic-corrupt-journal".utf8)
    try corruption.write(
        to: journalDirectory.appendingPathComponent("upgrade-journal.json"),
        options: .atomic
    )
    try corruption.write(
        to: journalDirectory.appendingPathComponent("upgrade-journal.previous.json"),
        options: .atomic
    )

    let binding = SyntheticSyncCoreBinding()
    let coordinator = try await configuredCoordinator(binding: binding).coordinator
    let status = await FailClosedUpgradePreparation.runIfPrepared(
        preparation: {
            _ = try UpgradeRollbackController(
                directory: journalDirectory,
                profileID: UUID()
            )
        },
        dataPlane: {
            await coordinator.synchronize(trigger: .appActivation)
        }
    )

    #expect(status == nil)
    #expect(await binding.synchronizeCalls == 0)
}

@Test func concurrentLifecycleCallsAwaitOneUpgradePreparationBeforeDataPlane() async throws {
    let profileID = UUID()
    let gate = UpgradePreparationSingleFlight()
    let preparation = BlockingUpgradePreparation()
    let dataPlane = DataPlaneEntryCounter()

    let lifecycle: @Sendable () async -> Bool = {
        do {
            try await gate.prepare(profileID: profileID) {
                try await preparation.run()
            }
            await dataPlane.enter()
            return true
        } catch {
            return false
        }
    }

    let first = Task { await lifecycle() }
    await preparation.waitUntilBlocked()
    let secondStarted = AsyncSignal()
    let second = Task {
        await secondStarted.signal()
        return await lifecycle()
    }
    await secondStarted.wait()
    var bothLifecycleCallsAreWaiting = false
    for _ in 0..<1_000 {
        if await gate.activeCallerCount() == 2 {
            bothLifecycleCallsAreWaiting = true
            break
        }
        await Task.yield()
    }

    #expect(bothLifecycleCallsAreWaiting)
    #expect(await preparation.invocationCount == 1)
    #expect(await dataPlane.entryCount == 0)
    await preparation.release()

    #expect(await first.value)
    #expect(await second.value)
    #expect(await preparation.invocationCount == 1)
    #expect(await dataPlane.entryCount == 2)
}

@Test func backgroundRetryGrowsCapsAndResetsAfterSuccess() async throws {
    let policy = BackgroundRetryPolicy(
        baseDelaySeconds: 10,
        maximumDelaySeconds: 40,
        jitterFraction: 0
    )
    let controller = try BackgroundSyncRetryController(
        directory: temporaryDirectory(),
        policy: policy,
        jitter: { _ in 0 }
    )

    #expect(try await controller.recordResult(success: false) == 10)
    #expect(try await controller.recordResult(success: false) == 20)
    #expect(try await controller.recordResult(success: false) == 40)
    #expect(try await controller.recordResult(success: false) == 40)
    #expect(try await controller.recordResult(success: true) == 10)
}

@Test func backgroundRetryUsesInjectedJitterAndPersistsChosenDelay() async throws {
    let root = temporaryDirectory()
    let policy = BackgroundRetryPolicy(
        baseDelaySeconds: 100,
        maximumDelaySeconds: 1_000,
        jitterFraction: 0.25
    )
    let controller = try BackgroundSyncRetryController(
        directory: root,
        policy: policy,
        jitter: { failureCount in failureCount == 1 ? 1 : -1 }
    )
    #expect(try await controller.recordResult(success: false) == 125)
    #expect(try await controller.recordResult(success: false) == 150)

    let reloaded = try BackgroundSyncRetryController(
        directory: root,
        policy: policy,
        jitter: { _ in 0 }
    )
    #expect(await reloaded.currentDelay() == 150)
}

@Test func remoteUnavailableIsRedactedAsConnectivity() async throws {
    let root = temporaryDirectory()
    let endpointStore = try EndpointProfileStore(directory: root.appendingPathComponent("state"))
    let profile = try SyncEndpointProfile(
        displayName: "Synthetic relay",
        endpoint: "https://sync.example.test",
        allowsPrivateHTTP: false
    )
    try await endpointStore.saveAndSelect(profile)
    let coordinator = MobileSyncCoordinator(
        endpoints: endpointStore,
        credentials: SyntheticCredentialStore(blobs: [profile.id: Data("opaque-fixture".utf8)]),
        binding: RemoteUnavailableBinding(),
        pathResolver: try syntheticPathResolver(root: root),
        statuses: try RedactedSyncStatusStore(directory: root.appendingPathComponent("state"))
    )

    let result = await coordinator.synchronize(trigger: .backgroundTask)
    #expect(result.phase == .failed)
    #expect(result.code == .connectivity)
}

@Test func coordinatorCancellationReachesTheActiveBindingOutOfBand() async throws {
    let binding = CancellationAwareBinding()
    let coordinator = try await configuredCoordinator(binding: binding).coordinator
    let work = Task { await coordinator.synchronize(trigger: .backgroundTask) }

    await binding.waitUntilStarted()
    await coordinator.cancelCurrentOperation()
    let result = await work.value

    #expect(result.phase == .failed)
    #expect(result.code == .cancelled)
    #expect(await binding.cancelCalls == 1)
}

@Test func activeFacadeRegistryCancelsBeforeRegistrationAndWhileRunning() throws {
    let registry = ActiveFacadeCancellationRegistry()
    let counter = LockedCounter()

    let beforeRegistration = try #require(registry.beginOperation())
    registry.cancelCurrentOperation()
    let registeredAfterCancellation = registry.registerFacade(
        for: beforeRegistration,
        cancellationAction: { counter.increment() }
    )
    #expect(registeredAfterCancellation)
    #expect(counter.value == 1)
    registry.finishOperation(beforeRegistration)

    let whileRunning = try #require(registry.beginOperation())
    let registeredBeforeCancellation = registry.registerFacade(
        for: whileRunning,
        cancellationAction: { counter.increment() }
    )
    #expect(registeredBeforeCancellation)
    registry.cancelCurrentOperation()
    #expect(counter.value == 2)
    registry.finishOperation(whileRunning)
    registry.cancelCurrentOperation()
    #expect(counter.value == 2)
}

@Test func generatedBoundaryPreservesDecodedRedactedErrors() {
    let decoded: [SyncCoreBindingError] = [
        .authorizationRequired,
        .remoteConflict,
        .remoteRejected,
        .remoteUnavailable,
        .networkUnavailable,
        .deadlineExceeded,
        .cancelled,
        .localState,
    ]
    for error in decoded {
        #expect(normalizeGeneratedBindingError(error) == error)
    }

    let native = NSError(
        domain: "synthetic.native",
        code: 1,
        userInfo: [NSLocalizedDescriptionKey: "remote_unavailable"]
    )
    #expect(normalizeGeneratedBindingError(native) == .remoteUnavailable)
}

@Test func generatedJSONBoundaryRejectsDuplicateKeysTrailingTokensAndInexactEnvelopes() throws {
    try GeneratedBindingJSONBoundary.validate(
        #"{"ok":true,"result":{"Recorded":true,"UseCount":1,"SyncEligible":true}}"#,
        kind: .learn
    )
    try GeneratedBindingJSONBoundary.validate(
        "  " + #"{"ok":false,"error_code":"remote_unavailable"}"# + "\n",
        kind: .learn
    )

    let rejected = [
        #"{"ok":true,"ok":false,"result":{"Recorded":true,"UseCount":1,"SyncEligible":true}}"#,
        #"{"ok":true,"\u006f\u006b":false,"result":{"Recorded":true,"UseCount":1,"SyncEligible":true}}"#,
        #"{"ok":true,"result":{"Recorded":true,"UseCount":1,"UseCount":2,"SyncEligible":true}}"#,
        #"{"ok":true,"result":{"Recorded":true,"UseCount":1,"SyncEligible":true},"result":null}"#,
        #"{"ok":false,"error_code":"remote_unavailable","error_code":"authorization_required"}"#,
        #"{"ok":false,"error_code":"remote_unavailable","error_message":"a","error_message":"b"}"#,
        #"{"ok":true,"result":{"Recorded":true,"UseCount":1,"SyncEligible":true}} {}"#,
        #"{"ok":true,"result":{"Recorded":true,"UseCount":1,"SyncEligible":true},"error_code":"cancelled"}"#,
        #"{"ok":false,"error_code":"cancelled","result":null}"#,
        #"{"ok":false,"error_code":"cancelled","error":"private detail"}"#,
        #"{"ok":true,"result":{"Recorded":true,"UseCount":1,"SyncEligible":true,"RawError":"private detail"}}"#,
        #"{"ok":true,"result":{"Recorded":true,"UseCount":1}}"#,
    ]
    for encoded in rejected {
        #expect(throws: SyncCoreBindingError.localState) {
            try GeneratedBindingJSONBoundary.validate(encoded, kind: .learn)
        }
    }
}

@Test func generatedResultValidatorRejectsNegativeCountersAndFreeFormGate() throws {
    try GeneratedBindingResultValidator.validateSync(
        rounds: 0,
        uploaded: 0,
        downloaded: 0,
        cursor: 0,
        snapshotRows: 0
    )
    try GeneratedBindingResultValidator.validateStatus(
        cursor: 0,
        controlPlaneGate: "signed_roster_chain_required"
    )
    try GeneratedBindingResultValidator.validateStatus(cursor: 0, controlPlaneGate: "")
    try GeneratedBindingResultValidator.validateSnapshot(rows: 0)

    #expect(throws: SyncCoreBindingError.localState) {
        try GeneratedBindingResultValidator.validateSync(
            rounds: -1,
            uploaded: 0,
            downloaded: 0,
            cursor: 0,
            snapshotRows: 0
        )
    }
    #expect(throws: SyncCoreBindingError.localState) {
        try GeneratedBindingResultValidator.validateSync(
            rounds: 0,
            uploaded: -1,
            downloaded: 0,
            cursor: 0,
            snapshotRows: 0
        )
    }
    #expect(throws: SyncCoreBindingError.localState) {
        try GeneratedBindingResultValidator.validateSync(
            rounds: 0,
            uploaded: 0,
            downloaded: -1,
            cursor: 0,
            snapshotRows: 0
        )
    }
    #expect(throws: SyncCoreBindingError.localState) {
        try GeneratedBindingResultValidator.validateSync(
            rounds: 0,
            uploaded: 0,
            downloaded: 0,
            cursor: -1,
            snapshotRows: 0
        )
    }
    #expect(throws: SyncCoreBindingError.localState) {
        try GeneratedBindingResultValidator.validateSync(
            rounds: 0,
            uploaded: 0,
            downloaded: 0,
            cursor: 0,
            snapshotRows: -1
        )
    }
    #expect(throws: SyncCoreBindingError.localState) {
        try GeneratedBindingResultValidator.validateStatus(
            cursor: 0,
            controlPlaneGate: "synthetic_unreviewed_gate"
        )
    }
    #expect(throws: SyncCoreBindingError.localState) {
        try GeneratedBindingResultValidator.validateSnapshot(rows: -1)
    }
}

@Test func everyCoordinatorOperationParticipatesInOneSingleFlightGate() async throws {
    for blockedOperation in BlockingSyncCoreBinding.Operation.allCases {
        let binding = BlockingSyncCoreBinding(blockedOperation: blockedOperation)
        let configured = try await configuredCoordinator(binding: binding)
        let coordinator = configured.coordinator
        let profileID = configured.profileID
        let active = Task {
            switch blockedOperation {
            case .synchronize:
                _ = await coordinator.synchronize(trigger: .manual)
            case .status:
                _ = await coordinator.refreshStatus(expectedProfileID: profileID)
            case .publish:
                _ = try? await coordinator.publishSnapshot()
            case .rollback:
                try? await coordinator.rollbackSnapshot(expectedProfileID: profileID)
            }
        }
        await binding.waitUntilBlocked()

        let syncBusy = await coordinator.synchronize(trigger: .manual)
        let statusBusy = await coordinator.refreshStatus(expectedProfileID: profileID)
        #expect(syncBusy.phase == .blocked && syncBusy.code == .operationBusy)
        #expect(statusBusy.phase == .blocked && statusBusy.code == .operationBusy)
        await #expect(throws: MobileSyncError.operationBusy) {
            try await coordinator.publishSnapshot()
        }
        await #expect(throws: MobileSyncError.operationBusy) {
            try await coordinator.rollbackSnapshot(expectedProfileID: profileID)
        }
        await #expect(throws: MobileSyncError.operationBusy) {
            try await coordinator.recordSelection(
                text: "合成忙碌选择",
                pinyin: "he cheng mang lu xuan ze",
                privacy: SelectionPrivacyContext(
                    passwordField: false,
                    privateMode: false,
                    oneTimeInput: false,
                    noPersonalizedLearning: false
                )
            )
        }
        await #expect(throws: MobileSyncError.operationBusy) {
            try await coordinator.saveExplicit(
                text: "合成忙碌保存",
                pinyin: "he cheng mang lu bao cun",
                useCount: 1,
                pinned: false
            )
        }
        await #expect(throws: MobileSyncError.operationBusy) {
            try await coordinator.delete(
                text: "合成忙碌删除",
                pinyin: "he cheng mang lu shan chu"
            )
        }
        let counts = await binding.counts()
        #expect(counts.total == 1)
        #expect(counts[blockedOperation] == 1)

        await binding.releaseBlockedOperation()
        await active.value
    }
}

@Test func profilesWithCredentialsUseDistinctDatabaseAndSnapshotPaths() async throws {
    let root = temporaryDirectory()
    let endpoints = try EndpointProfileStore(directory: root.appendingPathComponent("state"))
    let first = try SyncEndpointProfile(
        displayName: "Synthetic isolated one",
        endpoint: "https://one.example.test",
        allowsPrivateHTTP: false
    )
    let second = try SyncEndpointProfile(
        displayName: "Synthetic isolated two",
        endpoint: "https://two.example.test",
        allowsPrivateHTTP: false
    )
    try await endpoints.saveAndSelect(first)
    try await endpoints.saveAndSelect(second)
    let binding = PathCapturingBinding()
    let coordinator = MobileSyncCoordinator(
        endpoints: endpoints,
        credentials: SyntheticCredentialStore(blobs: [
            first.id: Data("opaque-fixture-one".utf8),
            second.id: Data("opaque-fixture-two".utf8),
        ]),
        binding: binding,
        pathResolver: try syntheticPathResolver(root: root),
        statuses: try RedactedSyncStatusStore(directory: root.appendingPathComponent("status"))
    )

    try await endpoints.select(first.id)
    #expect(await coordinator.synchronize(trigger: .manual).phase == .idle)
    try await endpoints.select(second.id)
    #expect(await coordinator.synchronize(trigger: .manual).phase == .idle)

    let captured = await binding.capturedPaths()
    let firstPaths = try #require(captured.first)
    let secondPaths = try #require(captured.last)
    #expect(firstPaths != secondPaths)
    #expect(firstPaths.encryptedDatabaseURL.path.contains(first.id.uuidString.lowercased()))
    #expect(firstPaths.privateSnapshotURL.path.contains(first.id.uuidString.lowercased()))
    #expect(secondPaths.encryptedDatabaseURL.path.contains(second.id.uuidString.lowercased()))
    #expect(secondPaths.privateSnapshotURL.path.contains(second.id.uuidString.lowercased()))

}

@Test func productionPathResolverRequiresReadableBackupExclusion() throws {
    let root = temporaryDirectory()
    do {
        let resolver = try SyncCorePathResolver(
            databaseRoot: root.appendingPathComponent("state/profiles", isDirectory: true),
            snapshotRoot: root.appendingPathComponent("shared/profiles", isDirectory: true)
        )
        let paths = try resolver.paths(for: UUID())
        for directory in [
            root.appendingPathComponent("state/profiles", isDirectory: true),
            root.appendingPathComponent("shared/profiles", isDirectory: true),
            paths.encryptedDatabaseURL.deletingLastPathComponent(),
            paths.privateSnapshotURL.deletingLastPathComponent(),
        ] {
            let values = try directory.resourceValues(forKeys: [.isExcludedFromBackupKey])
            #expect(values.isExcludedFromBackup == true)
        }
    } catch {
        // The current macOS SwiftPM temporary filesystem does not expose this
        // iOS backup key. Production initialization must fail closed there.
        #expect(error as? SyncCoreBindingError == .invalidConfiguration)
    }
}

@Test func profileSwitchDuringLocalHealthCannotMarkEitherProfileHealthy() async throws {
    let root = temporaryDirectory()
    let endpoints = try EndpointProfileStore(directory: root.appendingPathComponent("state"))
    let first = try SyncEndpointProfile(
        displayName: "Synthetic lease A",
        endpoint: "https://lease-a.example.test",
        allowsPrivateHTTP: false
    )
    let second = try SyncEndpointProfile(
        displayName: "Synthetic lease B",
        endpoint: "https://lease-b.example.test",
        allowsPrivateHTTP: false
    )
    try await endpoints.saveAndSelect(first)
    try await endpoints.saveAndSelect(second)
    try await endpoints.select(first.id)

    let resolver = try syntheticPathResolver(root: root)
    let firstPaths = try resolver.paths(for: first.id)
    try writeSyntheticSnapshot(at: firstPaths.privateSnapshotURL, text: "合成租约快照")
    let binding = BlockingSyncCoreBinding(blockedOperation: .status)
    let coordinator = MobileSyncCoordinator(
        endpoints: endpoints,
        credentials: SyntheticCredentialStore(blobs: [
            first.id: Data("opaque-fixture-a".utf8),
            second.id: Data("opaque-fixture-b".utf8),
        ]),
        binding: binding,
        pathResolver: resolver,
        statuses: try RedactedSyncStatusStore(directory: root.appendingPathComponent("status"))
    )
    let firstJournal = try UpgradeRollbackController(
        directory: try resolver.privateStateDirectory(for: first.id),
        profileID: first.id
    )
    let secondJournal = try UpgradeRollbackController(
        directory: try resolver.privateStateDirectory(for: second.id),
        profileID: second.id
    )

    let health = Task {
        await ProfileBoundUpgradeHealthVerifier.markHealthy(
            expectedProfileID: first.id,
            build: "synthetic-candidate",
            coordinator: coordinator,
            controller: firstJournal
        )
    }
    await binding.waitUntilBlocked()
    try await endpoints.select(second.id)
    await binding.releaseBlockedOperation()

    #expect(await health.value == false)
    for (controller, profileID) in [
        (firstJournal, first.id),
        (secondJournal, second.id),
    ] {
        _ = try await controller.beginLaunch(
            expectedProfileID: profileID,
            build: "synthetic-probe",
            validatedRollback: nil
        )
        let secondProbe = try await controller.beginLaunch(
            expectedProfileID: profileID,
            build: "synthetic-probe",
            validatedRollback: nil
        )
        // If either journal had been marked with an LKG, the second probe
        // without that exact rollback digest would be gated.
        #expect(secondProbe.gate == nil)
        #expect(!secondProbe.shouldRestoreSnapshot)
    }
}

@Test func upgradeHealthTransactionBlocksCompetingAtoBtoCPromotionsUntilCommit() async throws {
    let root = temporaryDirectory()
    let endpoints = try EndpointProfileStore(directory: root.appendingPathComponent("state"))
    let profile = try SyncEndpointProfile(
        displayName: "Synthetic snapshot transaction",
        endpoint: "https://snapshot-transaction.example.test",
        allowsPrivateHTTP: false
    )
    try await endpoints.saveAndSelect(profile)
    let resolver = try syntheticPathResolver(root: root)
    let paths = try resolver.paths(for: profile.id)
    try writeSyntheticSnapshot(at: paths.privateSnapshotURL, text: "合成版本甲")
    let fingerprintA = try ExactSnapshotInspector.inspect(at: paths.privateSnapshotURL)
    let inspector = BlockingSnapshotStateInspector()
    let binding = SnapshotPromotingBinding(promotions: ["合成版本乙", "合成版本丙"])
    let coordinator = MobileSyncCoordinator(
        endpoints: endpoints,
        credentials: SyntheticCredentialStore(blobs: [profile.id: Data("opaque-fixture".utf8)]),
        binding: binding,
        pathResolver: resolver,
        statuses: try RedactedSyncStatusStore(directory: root.appendingPathComponent("status")),
        snapshotStateInspector: { url in
            try await inspector.captureThenBlock(at: url)
        }
    )
    let journal = try UpgradeRollbackController(
        directory: try resolver.privateStateDirectory(for: profile.id),
        profileID: profile.id
    )

    let health = Task {
        await ProfileBoundUpgradeHealthVerifier.markHealthy(
            expectedProfileID: profile.id,
            build: "synthetic-health-a",
            coordinator: coordinator,
            controller: journal
        )
    }
    await inspector.waitUntilCaptured()
    #expect(await inspector.capturedState == .present(fingerprintA))

    // These are the competing B/C promotions that used to fit between the
    // exact A fingerprint and its journal commit. Both now fail closed while
    // the health transaction owns the coordinator claim.
    for _ in 0..<2 {
        await #expect(throws: MobileSyncError.operationBusy) {
            try await coordinator.publishSnapshot()
        }
    }
    let syncBusy = await coordinator.synchronize(trigger: .manual)
    let statusBusy = await coordinator.refreshStatus(expectedProfileID: profile.id)
    #expect(syncBusy.phase == .blocked && syncBusy.code == .operationBusy)
    #expect(statusBusy.phase == .blocked && statusBusy.code == .operationBusy)
    await #expect(throws: MobileSyncError.operationBusy) {
        try await coordinator.rollbackSnapshot(expectedProfileID: profile.id)
    }
    await #expect(throws: MobileSyncError.operationBusy) {
        try await coordinator.recordSelection(
            text: "合成竞争选择",
            pinyin: "he cheng jing zheng xuan ze",
            privacy: SelectionPrivacyContext(
                passwordField: false,
                privateMode: false,
                oneTimeInput: false,
                noPersonalizedLearning: false
            )
        )
    }
    await #expect(throws: MobileSyncError.operationBusy) {
        try await coordinator.saveExplicit(
            text: "合成竞争保存",
            pinyin: "he cheng jing zheng bao cun",
            useCount: 1,
            pinned: false
        )
    }
    await #expect(throws: MobileSyncError.operationBusy) {
        try await coordinator.delete(
            text: "合成竞争删除",
            pinyin: "he cheng jing zheng shan chu"
        )
    }
    #expect(await binding.publishCallCount == 0)
    #expect(try ExactSnapshotInspector.inspect(at: paths.privateSnapshotURL) == fingerprintA)

    await inspector.release()
    #expect(await health.value)
    #expect(try ExactSnapshotInspector.inspect(at: paths.privateSnapshotURL) == fingerprintA)

    _ = try await journal.beginLaunch(
        expectedProfileID: profile.id,
        build: "synthetic-probe",
        validatedRollback: fingerprintA
    )
    let exactJournalProbe = try await journal.beginLaunch(
        expectedProfileID: profile.id,
        build: "synthetic-probe",
        validatedRollback: fingerprintA
    )
    #expect(exactJournalProbe.shouldRestoreSnapshot)

    let promotedB = try await coordinator.publishSnapshot()
    let promotedC = try await coordinator.publishSnapshot()
    #expect(promotedB.generation == 1)
    #expect(promotedC.generation == 2)
    #expect(await binding.publishCallCount == 2)
    #expect(try ExactSnapshotInspector.inspect(at: paths.privateSnapshotURL) != fingerprintA)
}

@Test func profileSwitchWhileCredentialLoadsPreventsAutomaticRollback() async throws {
    let root = temporaryDirectory()
    let endpoints = try EndpointProfileStore(directory: root.appendingPathComponent("state"))
    let first = try SyncEndpointProfile(
        displayName: "Synthetic rollback A",
        endpoint: "https://rollback-a.example.test",
        allowsPrivateHTTP: false
    )
    let second = try SyncEndpointProfile(
        displayName: "Synthetic rollback B",
        endpoint: "https://rollback-b.example.test",
        allowsPrivateHTTP: false
    )
    try await endpoints.saveAndSelect(first)
    try await endpoints.saveAndSelect(second)
    try await endpoints.select(first.id)

    let credentials = BlockingCredentialStore(
        blockedProfileID: first.id,
        blobs: [
            first.id: Data("opaque-fixture-a".utf8),
            second.id: Data("opaque-fixture-b".utf8),
        ]
    )
    let binding = RollbackCapturingBinding()
    let coordinator = MobileSyncCoordinator(
        endpoints: endpoints,
        credentials: credentials,
        binding: binding,
        pathResolver: try syntheticPathResolver(root: root),
        statuses: try RedactedSyncStatusStore(directory: root.appendingPathComponent("status"))
    )

    let rollback = Task {
        do {
            try await coordinator.rollbackSnapshot(expectedProfileID: first.id)
            return false
        } catch MobileSyncError.profileChanged {
            return true
        } catch {
            return false
        }
    }
    await credentials.waitUntilLoadBlocks()
    try await endpoints.select(second.id)
    await credentials.releaseLoad()

    #expect(await rollback.value)
    #expect(await binding.rollbackProfileIDs().isEmpty)
}

@Test func productionBindingFactoryFailsClosedWithoutGeneratedModule() {
    #if !canImport(Mobilecore)
    #expect(SyncCoreBindingFactory.production() is UnavailableSyncCoreBinding)
    #endif
}

@Test func localHealthCanCommitBeforeAnOfflineRelayAttempt() async throws {
    let root = temporaryDirectory()
    let endpoints = try EndpointProfileStore(directory: root.appendingPathComponent("state"))
    let profile = try SyncEndpointProfile(
        displayName: "Synthetic offline relay",
        endpoint: "https://offline.example.test",
        allowsPrivateHTTP: false
    )
    try await endpoints.saveAndSelect(profile)
    let resolver = try syntheticPathResolver(root: root)
    let paths = try resolver.paths(for: profile.id)
    try writeSyntheticSnapshot(at: paths.privateSnapshotURL, text: "合成本地健康")
    let binding = LocalStatusOfflineSyncBinding()
    let coordinator = MobileSyncCoordinator(
        endpoints: endpoints,
        credentials: SyntheticCredentialStore(blobs: [profile.id: Data("opaque-fixture".utf8)]),
        binding: binding,
        pathResolver: resolver,
        statuses: try RedactedSyncStatusStore(directory: root.appendingPathComponent("status"))
    )
    let journal = try UpgradeRollbackController(
        directory: try resolver.privateStateDirectory(for: profile.id),
        profileID: profile.id
    )

    let markedHealthy = await ProfileBoundUpgradeHealthVerifier.markHealthy(
        expectedProfileID: profile.id,
        build: "synthetic-1",
        coordinator: coordinator,
        controller: journal
    )
    #expect(markedHealthy)
    let network = await coordinator.synchronize(trigger: .appActivation)
    #expect(network.code == .connectivity)
    let nextLaunch = try await journal.beginLaunch(
        expectedProfileID: profile.id,
        build: "synthetic-1",
        validatedRollback: nil
    )
    #expect(!nextLaunch.shouldRestoreSnapshot && nextLaunch.gate == nil)
    #expect(await binding.statusCalls == 1)
    #expect(await binding.syncCalls == 1)
}

@Test func freshProfileWithoutAnyLastKnownGoodNeverRequiresSnapshotRollback() async throws {
    let profileID = UUID()
    let controller = try UpgradeRollbackController(
        directory: temporaryDirectory(),
        profileID: profileID
    )

    let first = try await controller.beginLaunch(
        expectedProfileID: profileID,
        build: "synthetic-fresh-1",
        validatedRollback: nil
    )
    let second = try await controller.beginLaunch(
        expectedProfileID: profileID,
        build: "synthetic-fresh-1",
        validatedRollback: nil
    )

    #expect(first.gate == nil && !first.shouldRestoreSnapshot)
    #expect(second.gate == nil && !second.shouldRestoreSnapshot)
}

@Test func freshEmptyProfileRemainsRunnableAcrossTwoOfflineLaunches() async throws {
    let root = temporaryDirectory()
    let endpoints = try EndpointProfileStore(directory: root.appendingPathComponent("state"))
    let profile = try SyncEndpointProfile(
        displayName: "Synthetic fresh offline relay",
        endpoint: "https://fresh-offline.example.test",
        allowsPrivateHTTP: false
    )
    try await endpoints.saveAndSelect(profile)
    let resolver = try syntheticPathResolver(root: root)
    let paths = try resolver.paths(for: profile.id)
    let binding = LocalStatusOfflineSyncBinding(snapshotPresent: false)
    let coordinator = MobileSyncCoordinator(
        endpoints: endpoints,
        credentials: SyntheticCredentialStore(blobs: [profile.id: Data("opaque-fixture".utf8)]),
        binding: binding,
        pathResolver: resolver,
        statuses: try RedactedSyncStatusStore(directory: root.appendingPathComponent("status"))
    )
    let journalDirectory = try resolver.privateStateDirectory(for: profile.id)

    for _ in 0..<2 {
        let journal = try UpgradeRollbackController(
            directory: journalDirectory,
            profileID: profile.id
        )
        let plan = try await journal.beginLaunch(
            expectedProfileID: profile.id,
            build: "synthetic-fresh-1",
            validatedRollback: nil
        )
        #expect(plan.gate == nil && !plan.shouldRestoreSnapshot)

        let markedHealthy = await ProfileBoundUpgradeHealthVerifier.markHealthy(
            expectedProfileID: profile.id,
            build: "synthetic-fresh-1",
            coordinator: coordinator,
            controller: journal
        )
        #expect(markedHealthy)
        #expect(try ExactSnapshotInspector.inspectState(at: paths.privateSnapshotURL) == .absent)

        let network = await coordinator.synchronize(trigger: .appActivation)
        #expect(network.code == .connectivity)
        #expect(try ExactSnapshotInspector.inspectState(at: paths.privateSnapshotURL) == .absent)
    }

    #expect(await binding.statusCalls == 2)
    #expect(await binding.syncCalls == 2)
}

@Test func upgradeJournalRequestsSnapshotRecoveryAfterRepeatedUnhealthyLaunch() async throws {
    let profileID = UUID()
    let controller = try UpgradeRollbackController(
        directory: temporaryDirectory(),
        profileID: profileID
    )
    let knownGood = try syntheticFingerprint(text: "合成健康快照")
    _ = try await controller.markLocalDataPlaneHealthy(
        expectedProfileID: profileID,
        build: "synthetic-1",
        currentSnapshot: knownGood
    )
    let first = try await controller.beginLaunch(
        expectedProfileID: profileID,
        build: "synthetic-2",
        validatedRollback: knownGood
    )
    let second = try await controller.beginLaunch(
        expectedProfileID: profileID,
        build: "synthetic-2",
        validatedRollback: knownGood
    )
    #expect(!first.shouldRestoreSnapshot)
    #expect(first.gate == nil)
    #expect(second.shouldRestoreSnapshot)
    #expect(second.gate == nil)
}

@Test func mismatchedRollbackGatesUpgradeRecoveryWithoutRestoring() async throws {
    let profileID = UUID()
    let controller = try UpgradeRollbackController(
        directory: temporaryDirectory(),
        profileID: profileID
    )
    let knownGood = try syntheticFingerprint(text: "合成上一健康快照")
    let unrelated = try syntheticFingerprint(text: "合成不匹配回滚")
    _ = try await controller.markLocalDataPlaneHealthy(
        expectedProfileID: profileID,
        build: "synthetic-1",
        currentSnapshot: knownGood
    )
    _ = try await controller.beginLaunch(
        expectedProfileID: profileID,
        build: "synthetic-2",
        validatedRollback: unrelated
    )
    let nextLaunch = try await controller.beginLaunch(
        expectedProfileID: profileID,
        build: "synthetic-2",
        validatedRollback: unrelated
    )
    #expect(!nextLaunch.shouldRestoreSnapshot)
    #expect(nextLaunch.gate == .matchingValidatedRollbackRequired)
}

@Test func healthyJournalBindsBuildToMonotonicSnapshotGenerationAndDigest() async throws {
    let profileID = UUID()
    let controller = try UpgradeRollbackController(
        directory: temporaryDirectory(),
        profileID: profileID
    )
    let firstFingerprint = try syntheticFingerprint(text: "合成第一代")
    let secondFingerprint = try syntheticFingerprint(text: "合成第二代")

    let first = try await controller.markLocalDataPlaneHealthy(
        expectedProfileID: profileID,
        build: "synthetic-1",
        currentSnapshot: firstFingerprint
    )
    let unchanged = try await controller.markLocalDataPlaneHealthy(
        expectedProfileID: profileID,
        build: "synthetic-1",
        currentSnapshot: firstFingerprint
    )
    let second = try await controller.markLocalDataPlaneHealthy(
        expectedProfileID: profileID,
        build: "synthetic-2",
        currentSnapshot: secondFingerprint
    )

    #expect(first.generation == 1)
    #expect(unchanged == first)
    #expect(second.generation == 2)
    #expect(second.sha256Hex == secondFingerprint.sha256Hex)
    await #expect(throws: UpgradeRollbackError.snapshotNotValidated) {
        try await controller.markLocalEmptyDataPlaneHealthy(
            expectedProfileID: profileID,
            build: "synthetic-3"
        )
    }
}

@Test func redactedStatusCannotSerializeSensitiveFreeFormValues() throws {
    let status = RedactedSyncStatus(
        phase: .blocked,
        code: .syncCoreUnavailable,
        pendingEnvelopeCount: 2,
        cursor: 5
    )
    let encoded = try JSONEncoder().encode(status)
    let text = try #require(String(data: encoded, encoding: .utf8))
    #expect(!text.contains("credential-sentinel"))
    #expect(!text.contains("https://"))
    #expect(!text.contains("phrase"))
}

private actor SyntheticCredentialStore: OpaqueCredentialStore {
    var blobs: [UUID: Data]
    init(blobs: [UUID: Data]) { self.blobs = blobs }
    func load(for profileID: UUID) -> Data? { blobs[profileID] }
    func save(_ credentialBlob: Data, for profileID: UUID) { blobs[profileID] = credentialBlob }
}

private actor AsyncSignal {
    private var signalled = false
    private var waiters: [CheckedContinuation<Void, Never>] = []

    func signal() {
        signalled = true
        let pending = waiters
        waiters.removeAll()
        pending.forEach { $0.resume() }
    }

    func wait() async {
        if signalled { return }
        await withCheckedContinuation { waiters.append($0) }
    }
}

private actor BlockingUpgradePreparation {
    private var blocked = false
    private var startWaiters: [CheckedContinuation<Void, Never>] = []
    private var releaseWaiter: CheckedContinuation<Void, Never>?
    private(set) var invocationCount = 0

    func waitUntilBlocked() async {
        if blocked { return }
        await withCheckedContinuation { startWaiters.append($0) }
    }

    func run() async throws {
        invocationCount += 1
        blocked = true
        let pending = startWaiters
        startWaiters.removeAll()
        pending.forEach { $0.resume() }
        await withCheckedContinuation { releaseWaiter = $0 }
    }

    func release() {
        releaseWaiter?.resume()
        releaseWaiter = nil
    }
}

private actor DataPlaneEntryCounter {
    private(set) var entryCount = 0
    func enter() { entryCount += 1 }
}

private actor BlockingCredentialStore: OpaqueCredentialStore {
    private let blockedProfileID: UUID
    private var blobs: [UUID: Data]
    private var didBlock = false
    private var startWaiters: [CheckedContinuation<Void, Never>] = []
    private var releaseWaiter: CheckedContinuation<Void, Never>?

    init(blockedProfileID: UUID, blobs: [UUID: Data]) {
        self.blockedProfileID = blockedProfileID
        self.blobs = blobs
    }

    func waitUntilLoadBlocks() async {
        if didBlock { return }
        await withCheckedContinuation { startWaiters.append($0) }
    }

    func releaseLoad() {
        releaseWaiter?.resume()
        releaseWaiter = nil
    }

    func load(for profileID: UUID) async -> Data? {
        if profileID == blockedProfileID, !didBlock {
            didBlock = true
            let waiters = startWaiters
            startWaiters.removeAll()
            waiters.forEach { $0.resume() }
            await withCheckedContinuation { releaseWaiter = $0 }
        }
        return blobs[profileID]
    }

    func save(_ credentialBlob: Data, for profileID: UUID) {
        blobs[profileID] = credentialBlob
    }
}

private actor RollbackCapturingBinding: SyncCoreBinding {
    private var rollbackIDs: [UUID] = []

    func rollbackProfileIDs() -> [UUID] { rollbackIDs }

    func synchronize(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreReport {
        SyncCoreReport(rounds: 1, uploaded: 0, downloaded: 0, cursor: 0, pending: 0, snapshotRows: 0, snapshotChanged: false)
    }

    func status(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreStatus {
        SyncCoreStatus(cursor: 0, pending: 0, prepared: false, snapshotPresent: false, rollbackPresent: false, controlPlaneGate: "signed_roster_chain_required")
    }

    func publishSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SnapshotPublishReport {
        SnapshotPublishReport(generation: 1, rows: 0, changed: false, rollbackAvailable: false)
    }

    func rollbackSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data) async throws {
        rollbackIDs.append(endpoint.id)
    }
}

private actor SyntheticSyncCoreBinding: SyncCoreBinding {
    var synchronizeCalls = 0

    func synchronize(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreReport {
        synchronizeCalls += 1
        #expect(credentialBlob == Data("opaque-fixture".utf8))
        return SyncCoreReport(rounds: 1, uploaded: 1, downloaded: 1, cursor: 7, pending: 2, snapshotRows: 3, snapshotChanged: true)
    }

    func status(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreStatus {
        SyncCoreStatus(cursor: 7, pending: 2, prepared: false, snapshotPresent: true, rollbackPresent: true, controlPlaneGate: "signed_roster_chain_required")
    }

    func publishSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SnapshotPublishReport {
        SnapshotPublishReport(generation: 1, rows: 3, changed: true, rollbackAvailable: false)
    }

    func rollbackSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data) async throws {}
}

private actor LocalMutationCapturingBinding: SyncCoreBinding {
    struct Capture: Sendable {
        let recordSelectionContexts: [SelectionPrivacyContext]
        let saveExplicitCount: Int
        let savedUseCount: UInt64?
        let savedPinned: Bool?
        let deleteCount: Int
        let publishCount: Int
    }

    private var recordSelectionContexts: [SelectionPrivacyContext] = []
    private var saveExplicitCount = 0
    private var savedUseCount: UInt64?
    private var savedPinned: Bool?
    private var deleteCount = 0
    private var publishCount = 0

    func capture() -> Capture {
        Capture(
            recordSelectionContexts: recordSelectionContexts,
            saveExplicitCount: saveExplicitCount,
            savedUseCount: savedUseCount,
            savedPinned: savedPinned,
            deleteCount: deleteCount,
            publishCount: publishCount
        )
    }

    func synchronize(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreReport {
        SyncCoreReport(rounds: 1, uploaded: 0, downloaded: 0, cursor: 1, pending: 0, snapshotRows: 0, snapshotChanged: false)
    }

    func status(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreStatus {
        SyncCoreStatus(cursor: 1, pending: 0, prepared: false, snapshotPresent: true, rollbackPresent: false, controlPlaneGate: "signed_roster_chain_required")
    }

    func publishSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SnapshotPublishReport {
        publishCount += 1
        return SnapshotPublishReport(generation: 1, rows: 1, changed: true, rollbackAvailable: false)
    }

    func rollbackSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data) async throws {}

    func recordSelection(
        paths: SyncCorePaths,
        endpoint: SyncEndpointProfile,
        credentialBlob: Data,
        text: String,
        pinyin: String,
        privacy: SelectionPrivacyContext,
        timeoutMilliseconds: Int64
    ) async throws -> RecordSelectionReport {
        recordSelectionContexts.append(privacy)
        let permitted = !privacy.passwordField
            && !privacy.privateMode
            && !privacy.oneTimeInput
            && !privacy.noPersonalizedLearning
        return RecordSelectionReport(
            recorded: permitted,
            useCount: permitted ? 1 : 0,
            syncEligible: permitted
        )
    }

    func saveExplicit(
        paths: SyncCorePaths,
        endpoint: SyncEndpointProfile,
        credentialBlob: Data,
        text: String,
        pinyin: String,
        useCount: UInt64,
        pinned: Bool,
        timeoutMilliseconds: Int64
    ) async throws {
        saveExplicitCount += 1
        savedUseCount = useCount
        savedPinned = pinned
    }

    func delete(
        paths: SyncCorePaths,
        endpoint: SyncEndpointProfile,
        credentialBlob: Data,
        text: String,
        pinyin: String,
        timeoutMilliseconds: Int64
    ) async throws {
        deleteCount += 1
    }
}

private actor PathCapturingBinding: SyncCoreBinding {
    private var pathsSeen: [SyncCorePaths] = []

    func capturedPaths() -> [SyncCorePaths] { pathsSeen }

    func synchronize(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreReport {
        pathsSeen.append(paths)
        return SyncCoreReport(rounds: 1, uploaded: 0, downloaded: 0, cursor: 1, pending: 0, snapshotRows: 0, snapshotChanged: false)
    }
    func status(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreStatus {
        SyncCoreStatus(cursor: 1, pending: 0, prepared: false, snapshotPresent: true, rollbackPresent: false, controlPlaneGate: "signed_roster_chain_required")
    }
    func publishSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SnapshotPublishReport {
        SnapshotPublishReport(generation: 1, rows: 0, changed: false, rollbackAvailable: false)
    }
    func rollbackSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data) async throws {}
}

private actor BlockingSyncCoreBinding: SyncCoreBinding {
    enum Operation: CaseIterable, Hashable, Sendable {
        case synchronize
        case status
        case publish
        case rollback
    }

    struct Counts: Sendable {
        private let values: [Operation: Int]
        init(values: [Operation: Int]) { self.values = values }
        var total: Int { values.values.reduce(0, +) }
        subscript(operation: Operation) -> Int { values[operation, default: 0] }
    }

    private let blockedOperation: Operation
    private var callCounts: [Operation: Int] = [:]
    private var didBlock = false
    private var startWaiters: [CheckedContinuation<Void, Never>] = []
    private var releaseWaiter: CheckedContinuation<Void, Never>?

    init(blockedOperation: Operation) {
        self.blockedOperation = blockedOperation
    }

    func waitUntilBlocked() async {
        if didBlock { return }
        await withCheckedContinuation { startWaiters.append($0) }
    }

    func releaseBlockedOperation() {
        releaseWaiter?.resume()
        releaseWaiter = nil
    }

    func counts() -> Counts { Counts(values: callCounts) }

    func synchronize(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreReport {
        await recordAndBlock(.synchronize)
        return SyncCoreReport(rounds: 1, uploaded: 0, downloaded: 0, cursor: 1, pending: 0, snapshotRows: 0, snapshotChanged: false)
    }
    func status(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreStatus {
        await recordAndBlock(.status)
        return SyncCoreStatus(cursor: 1, pending: 0, prepared: false, snapshotPresent: true, rollbackPresent: false, controlPlaneGate: "signed_roster_chain_required")
    }
    func publishSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SnapshotPublishReport {
        await recordAndBlock(.publish)
        return SnapshotPublishReport(generation: 1, rows: 0, changed: false, rollbackAvailable: false)
    }
    func rollbackSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data) async throws {
        await recordAndBlock(.rollback)
    }

    private func recordAndBlock(_ operation: Operation) async {
        callCounts[operation, default: 0] += 1
        guard operation == blockedOperation else { return }
        didBlock = true
        let waiters = startWaiters
        startWaiters.removeAll()
        waiters.forEach { $0.resume() }
        await withCheckedContinuation { releaseWaiter = $0 }
    }
}

private actor BlockingSnapshotStateInspector {
    private var captured = false
    private var captureWaiters: [CheckedContinuation<Void, Never>] = []
    private var releaseWaiter: CheckedContinuation<Void, Never>?
    private(set) var capturedState: ExactSnapshotState?

    func waitUntilCaptured() async {
        if captured { return }
        await withCheckedContinuation { captureWaiters.append($0) }
    }

    func captureThenBlock(at url: URL) async throws -> ExactSnapshotState {
        let state = try ExactSnapshotInspector.inspectState(at: url)
        capturedState = state
        captured = true
        let pending = captureWaiters
        captureWaiters.removeAll()
        pending.forEach { $0.resume() }
        await withCheckedContinuation { releaseWaiter = $0 }
        return state
    }

    func release() {
        releaseWaiter?.resume()
        releaseWaiter = nil
    }
}

private actor SnapshotPromotingBinding: SyncCoreBinding {
    private let promotions: [String]
    private(set) var publishCallCount = 0

    init(promotions: [String]) {
        self.promotions = promotions
    }

    func synchronize(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreReport {
        SyncCoreReport(rounds: 1, uploaded: 0, downloaded: 0, cursor: 1, pending: 0, snapshotRows: 1, snapshotChanged: false)
    }

    func status(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreStatus {
        SyncCoreStatus(cursor: 1, pending: 0, prepared: false, snapshotPresent: true, rollbackPresent: true, controlPlaneGate: "signed_roster_chain_required")
    }

    func publishSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SnapshotPublishReport {
        guard publishCallCount < promotions.count else {
            throw SyncCoreBindingError.localState
        }
        try writeSyntheticSnapshot(
            at: paths.privateSnapshotURL,
            text: promotions[publishCallCount]
        )
        publishCallCount += 1
        return SnapshotPublishReport(
            generation: UInt64(publishCallCount),
            rows: 1,
            changed: true,
            rollbackAvailable: true
        )
    }

    func rollbackSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data) async throws {}
}

private actor CancellationAwareBinding: SyncCoreBinding {
    private var started = false
    private var cancelled = false
    private var startWaiters: [CheckedContinuation<Void, Never>] = []
    private var operationWaiter: CheckedContinuation<Void, Never>?
    private(set) var cancelCalls = 0

    func waitUntilStarted() async {
        if started { return }
        await withCheckedContinuation { startWaiters.append($0) }
    }

    func synchronize(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreReport {
        started = true
        let waiters = startWaiters
        startWaiters.removeAll()
        waiters.forEach { $0.resume() }
        await withCheckedContinuation { continuation in
            if cancelled {
                continuation.resume()
            } else {
                operationWaiter = continuation
            }
        }
        if cancelled { throw SyncCoreBindingError.cancelled }
        return SyncCoreReport(rounds: 1, uploaded: 0, downloaded: 0, cursor: 0, pending: 0, snapshotRows: 0, snapshotChanged: false)
    }

    func cancelCurrentOperation() async {
        cancelCalls += 1
        cancelled = true
        operationWaiter?.resume()
        operationWaiter = nil
    }

    func status(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreStatus {
        SyncCoreStatus(cursor: 0, pending: 0, prepared: false, snapshotPresent: false, rollbackPresent: false, controlPlaneGate: "signed_roster_chain_required")
    }

    func publishSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SnapshotPublishReport {
        SnapshotPublishReport(generation: 1, rows: 0, changed: false, rollbackAvailable: false)
    }

    func rollbackSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data) async throws {}
}

private final class LockedCounter: @unchecked Sendable {
    private let lock = NSLock()
    private var count = 0

    var value: Int {
        lock.lock()
        defer { lock.unlock() }
        return count
    }

    func increment() {
        lock.lock()
        count += 1
        lock.unlock()
    }
}

private actor LocalStatusOfflineSyncBinding: SyncCoreBinding {
    private let snapshotPresent: Bool
    var statusCalls = 0
    var syncCalls = 0

    init(snapshotPresent: Bool = true) {
        self.snapshotPresent = snapshotPresent
    }

    func synchronize(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreReport {
        syncCalls += 1
        throw SyncCoreBindingError.remoteUnavailable
    }
    func status(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreStatus {
        statusCalls += 1
        return SyncCoreStatus(cursor: 1, pending: 0, prepared: false, snapshotPresent: snapshotPresent, rollbackPresent: false, controlPlaneGate: "signed_roster_chain_required")
    }
    func publishSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SnapshotPublishReport {
        throw SyncCoreBindingError.remoteUnavailable
    }
    func rollbackSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data) async throws {
        throw SyncCoreBindingError.remoteUnavailable
    }
}

private struct RemoteUnavailableBinding: SyncCoreBinding {
    func synchronize(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreReport {
        throw SyncCoreBindingError.remoteUnavailable
    }
    func status(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SyncCoreStatus {
        throw SyncCoreBindingError.remoteUnavailable
    }
    func publishSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data, timeoutMilliseconds: Int64) async throws -> SnapshotPublishReport {
        throw SyncCoreBindingError.remoteUnavailable
    }
    func rollbackSnapshot(paths: SyncCorePaths, endpoint: SyncEndpointProfile, credentialBlob: Data) async throws {
        throw SyncCoreBindingError.remoteUnavailable
    }
}

private func temporaryDirectory() -> URL {
    FileManager.default.temporaryDirectory
        .appendingPathComponent("yunpin-ios-tests")
        .appendingPathComponent(UUID().uuidString)
}

private func syntheticPathResolver(root: URL) throws -> SyncCorePathResolver {
    try SyncCorePathResolver(
        databaseRoot: root.appendingPathComponent("state/profiles", isDirectory: true),
        snapshotRoot: root.appendingPathComponent("shared/profiles", isDirectory: true),
        directoryPreparer: { directory in
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            let identity = try directory.resourceValues(forKeys: [.isDirectoryKey, .isSymbolicLinkKey])
            guard identity.isDirectory == true, identity.isSymbolicLink != true else {
                throw SyncCoreBindingError.invalidConfiguration
            }
        }
    )
}

private struct ConfiguredCoordinator {
    let coordinator: MobileSyncCoordinator
    let profileID: UUID
}

private func configuredCoordinator(binding: any SyncCoreBinding) async throws -> ConfiguredCoordinator {
    let root = temporaryDirectory()
    let endpoints = try EndpointProfileStore(directory: root.appendingPathComponent("state"))
    let profile = try SyncEndpointProfile(
        displayName: "Synthetic single flight",
        endpoint: "https://sync.example.test",
        allowsPrivateHTTP: false
    )
    try await endpoints.saveAndSelect(profile)
    return ConfiguredCoordinator(
        coordinator: MobileSyncCoordinator(
            endpoints: endpoints,
            credentials: SyntheticCredentialStore(blobs: [profile.id: Data("opaque-fixture".utf8)]),
            binding: binding,
            pathResolver: try syntheticPathResolver(root: root),
            statuses: try RedactedSyncStatusStore(directory: root.appendingPathComponent("status"))
        ),
        profileID: profile.id
    )
}

private func syntheticFingerprint(text: String) throws -> ValidatedSnapshotFingerprint {
    let root = temporaryDirectory()
    let url = root.appendingPathComponent("synthetic.tsv")
    try writeSyntheticSnapshot(at: url, text: text)
    return try ExactSnapshotInspector.inspect(at: url)
}

private func writeSyntheticSnapshot(at url: URL, text: String) throws {
    try FileManager.default.createDirectory(
        at: url.deletingLastPathComponent(),
        withIntermediateDirectories: true
    )
    let data = Data((PrivateSnapshotValidator.header
        + "\(text)\the cheng\tsynced_learning@20000\t1\tfalse\n").utf8)
    try data.write(to: url, options: .atomic)
}
